package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	agentv1 "github.com/znicelya/ajiasu-proxy/internal/gen/agent/v1"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CommandSink interface {
	Deliver(context.Context, uuid.UUID, *agentv1.ControlMessage) error
}

type Worker struct {
	pools    *database.Pools
	sink     CommandSink
	owner    uuid.UUID
	period   time.Duration
	provider secrets.Provider
}

func NewWorker(pools *database.Pools, sink CommandSink, providers ...secrets.Provider) (*Worker, error) {
	if pools == nil || pools.Platform == nil || sink == nil {
		return nil, ErrInvalidArgument
	}
	var provider secrets.Provider
	if len(providers) > 0 {
		provider = providers[0]
	}
	return &Worker{pools: pools, sink: sink, owner: uuid.New(), period: 500 * time.Millisecond, provider: provider}, nil
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.period)
	defer ticker.Stop()
	for {
		_ = w.Step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) Step(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := w.pools.Platform.Exec(ctx, `UPDATE nodes.nodes SET connectivity_state=CASE WHEN last_heartbeat_at < $1 THEN 'offline' WHEN last_heartbeat_at < $2 THEN 'stale' ELSE connectivity_state END,updated_at=$3 WHERE last_heartbeat_at IS NOT NULL AND connectivity_state<>'offline'`, now.Add(-5*time.Minute), now.Add(-45*time.Second), now); err != nil {
		return err
	}
	if _, err := w.pools.Platform.Exec(ctx, `UPDATE accounts.account_capacity_reservations r SET expires_at=$1 FROM reconciler.runner_desired_states d WHERE d.tenant_id=r.tenant_id AND d.capacity_reservation_id=r.id AND r.expires_at<$2`, now.Add(24*time.Hour), now.Add(12*time.Hour)); err != nil {
		return err
	}
	deadline := now.Add(30 * time.Second)
	var work struct {
		id, tenantID, resourceID, operationID, leaseOwner uuid.UUID
		nodeID, runnerID, endpointID, accountID           uuid.UUID
		generation, credentialVersion                     int64
		action                                            string
		runtimeSpec                                       []byte
	}
	_, err := database.InPlatformTx(ctx, w.pools.Platform, w.owner, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		row := tx.QueryRow(ctx, `WITH candidate AS (SELECT id FROM reconciler.work_items WHERE available_at <= $1 AND (lease_owner IS NULL OR lease_deadline <= $1) ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE reconciler.work_items w SET lease_owner=$2,lease_deadline=$3,attempts=w.attempts+1,updated_at=$1 FROM candidate c WHERE w.id=c.id RETURNING w.id,w.tenant_id,w.resource_id,w.operation_id,w.action,w.generation`, now, w.owner, deadline)
		var tenantID *uuid.UUID
		if err := row.Scan(&work.id, &tenantID, &work.resourceID, &work.operationID, &work.action, &work.generation); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return struct{}{}, nil
			}
			return struct{}{}, err
		}
		if tenantID == nil {
			return struct{}{}, ErrNotFound
		}
		work.tenantID = *tenantID
		if err := tx.QueryRow(ctx, `SELECT node_id,runner_id,endpoint_id,account_id,credential_version,runtime_spec FROM reconciler.runner_desired_states WHERE tenant_id=$1 AND endpoint_id=$2`, work.tenantID, work.resourceID).Scan(&work.nodeID, &work.runnerID, &work.endpointID, &work.accountID, &work.credentialVersion, &work.runtimeSpec); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil || work.id == uuid.Nil {
		return err
	}
	command := commandFromWork(work)
	if w.provider != nil && (work.action == "create" || work.action == "rebuild") {
		var sealed secrets.SealedSecret
		queryErr := w.pools.Platform.QueryRow(ctx, `SELECT provider,key_id,ciphertext,wrapped_dek,external_ref FROM accounts.account_credentials WHERE tenant_id=$1 AND account_id=$2 AND version=$3`, work.tenantID, work.accountID, work.credentialVersion).Scan(&sealed.Provider, &sealed.KeyID, &sealed.Ciphertext, &sealed.WrappedDEK, &sealed.ExternalRef)
		if queryErr != nil {
			_ = releaseWork(ctx, w, work.id, "credential_unavailable", retryDelay(work.generation))
			return queryErr
		}
		plaintext, openErr := w.provider.Open(ctx, secrets.Context{TenantID: work.tenantID, AccountID: work.accountID, Version: work.credentialVersion, Purpose: secrets.AccountCredentialPurpose}, sealed)
		if openErr != nil {
			_ = releaseWork(ctx, w, work.id, "credential_unavailable", retryDelay(work.generation))
			return openErr
		}
		command.GetRunnerCommand().CredentialConfiguration = plaintext
		deliverErr := w.sink.Deliver(ctx, work.nodeID, command)
		clear(plaintext)
		if deliverErr != nil {
			_ = releaseWork(ctx, w, work.id, "stream_unavailable", retryDelay(work.generation))
			return deliverErr
		}
		return releaseWork(ctx, w, work.id, "delivered", 30*time.Second)
	}
	deliverErr := w.sink.Deliver(ctx, work.nodeID, command)
	available := time.Now().UTC().Add(30 * time.Second)
	if deliverErr != nil {
		available = time.Now().UTC().Add(retryDelay(work.generation))
	}
	_, updateErr := w.pools.Platform.Exec(ctx, `UPDATE reconciler.work_items SET lease_owner=NULL,lease_deadline=NULL,available_at=$1,last_result_code=$2,updated_at=$3 WHERE id=$4 AND lease_owner=$5`, available, resultCode(deliverErr), time.Now().UTC(), work.id, w.owner)
	if deliverErr != nil {
		return deliverErr
	}
	return updateErr
}

func releaseWork(ctx context.Context, w *Worker, id uuid.UUID, code string, delay time.Duration) error {
	_, err := w.pools.Platform.Exec(ctx, `UPDATE reconciler.work_items SET lease_owner=NULL,lease_deadline=NULL,available_at=$1,last_result_code=$2,updated_at=$3 WHERE id=$4 AND lease_owner=$5`, time.Now().UTC().Add(delay), code, time.Now().UTC(), id, w.owner)
	return err
}

func commandFromWork(work struct {
	id, tenantID, resourceID, operationID, leaseOwner uuid.UUID
	nodeID, runnerID, endpointID, accountID           uuid.UUID
	generation, credentialVersion                     int64
	action                                            string
	runtimeSpec                                       []byte
}) *agentv1.ControlMessage {
	var spec struct {
		RunnerImage      string            `json:"runner_image"`
		MemoryLimitBytes uint64            `json:"memory_limit_bytes"`
		CPUQuotaMillis   uint32            `json:"cpu_quota_millis"`
		ImmutableLabels  map[string]string `json:"immutable_labels"`
	}
	_ = json.Unmarshal(work.runtimeSpec, &spec)
	action := agentv1.RunnerAction_RUNNER_ACTION_CREATE
	switch work.action {
	case "stop":
		action = agentv1.RunnerAction_RUNNER_ACTION_STOP
	case "rebuild":
		action = agentv1.RunnerAction_RUNNER_ACTION_REBUILD
	case "garbage_collect":
		action = agentv1.RunnerAction_RUNNER_ACTION_GARBAGE_COLLECT
	}
	return &agentv1.ControlMessage{Body: &agentv1.ControlMessage_RunnerCommand{RunnerCommand: &agentv1.RunnerCommand{OperationId: work.operationID.String(), RunnerId: work.runnerID.String(), TenantId: work.tenantID.String(), EndpointId: work.endpointID.String(), AccountId: work.accountID.String(), CredentialVersion: uint64(work.credentialVersion), DesiredGeneration: uint64(work.generation), Action: action, Deadline: time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339Nano), Runtime: &agentv1.RuntimeSpec{RunnerImage: spec.RunnerImage, MemoryLimitBytes: spec.MemoryLimitBytes, CpuQuotaMillis: spec.CPUQuotaMillis, ImmutableLabels: spec.ImmutableLabels}}}}
}

func retryDelay(generation int64) time.Duration {
	if generation < 1 {
		generation = 1
	}
	if generation > 8 {
		generation = 8
	}
	return time.Duration(1<<generation) * time.Second
}
func resultCode(err error) string {
	if err == nil {
		return "delivered"
	}
	return "stream_unavailable"
}
