package reconciler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrForbidden       = errors.New("reconciliation operation is forbidden")
	ErrInvalidArgument = errors.New("invalid reconciliation argument")
	ErrNotFound        = errors.New("runner desired state was not found")
	ErrStale           = errors.New("runner observation is stale")
	ErrStorage         = errors.New("reconciliation storage failure")
)

type RunnerState string

const (
	RunnerRunning  RunnerState = "running"
	RunnerStopped  RunnerState = "stopped"
	RunnerFailed   RunnerState = "failed"
	RunnerOrphaned RunnerState = "orphaned"
)

type Observation struct {
	NodeID       uuid.UUID
	RunnerID     uuid.UUID
	OperationID  uuid.UUID
	Generation   int64
	State        RunnerState
	ReasonCode   string
	RestartCount int
	ObservedAt   time.Time
	Metadata     tenancy.ActorMetadata
}
type Service struct {
	pools *database.Pools
	audit audit.Service
	now   func() time.Time
}

func NewService(pools *database.Pools, auditService audit.Service) (*Service, error) {
	if pools == nil || pools.Platform == nil {
		return nil, ErrInvalidArgument
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &Service{pools: pools, audit: auditService, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) ApplyObservation(ctx context.Context, o Observation) error {
	if o.NodeID == uuid.Nil || o.RunnerID == uuid.Nil || o.OperationID == uuid.Nil || o.Generation < 1 || !validState(o.State) || len(strings.TrimSpace(o.ReasonCode)) == 0 || len(strings.TrimSpace(o.ReasonCode)) > 128 || o.RestartCount < 0 || o.ObservedAt.IsZero() {
		return ErrInvalidArgument
	}
	_, err := database.InPlatformTx(ctx, s.pools.Platform, o.NodeID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		var tenantID, endpointID, nodeID, operationID uuid.UUID
		var generation int64
		var reservationID *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT tenant_id,endpoint_id,node_id,operation_id,desired_generation,capacity_reservation_id FROM reconciler.runner_desired_states WHERE runner_id=$1 FOR UPDATE`, o.RunnerID).Scan(&tenantID, &endpointID, &nodeID, &operationID, &generation, &reservationID); err != nil {
			return struct{}{}, mapError(err)
		}
		if nodeID != o.NodeID || operationID != o.OperationID || generation != o.Generation {
			return struct{}{}, ErrStale
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `INSERT INTO reconciler.runner_observations (runner_id,tenant_id,endpoint_id,node_id,operation_id,observed_generation,observed_state,reason_code,restart_count,observed_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (runner_id) DO UPDATE SET operation_id=EXCLUDED.operation_id,observed_generation=EXCLUDED.observed_generation,observed_state=EXCLUDED.observed_state,reason_code=EXCLUDED.reason_code,restart_count=EXCLUDED.restart_count,observed_at=EXCLUDED.observed_at,updated_at=EXCLUDED.updated_at WHERE reconciler.runner_observations.observed_generation<=EXCLUDED.observed_generation`, o.RunnerID, tenantID, endpointID, o.NodeID, o.OperationID, o.Generation, o.State, strings.TrimSpace(o.ReasonCode), o.RestartCount, o.ObservedAt.UTC(), now); err != nil {
			return struct{}{}, mapError(err)
		}
		status := string(o.State)
		if _, err := tx.Exec(ctx, `UPDATE endpoints.endpoint_status SET observed_generation=$1,observed_state=$2,runner_id=$3,reason_code=$4,last_agent_observation_at=$5,last_transition_at=CASE WHEN observed_state<>$2 THEN $5 ELSE last_transition_at END,updated_at=$6 WHERE tenant_id=$7 AND endpoint_id=$8`, o.Generation, status, o.RunnerID, strings.TrimSpace(o.ReasonCode), o.ObservedAt.UTC(), now, tenantID, endpointID); err != nil {
			return struct{}{}, mapError(err)
		}
		if o.State == RunnerRunning {
			if _, err := tx.Exec(ctx, `UPDATE scheduler.endpoint_assignments SET state='assigned',health_state='healthy',account_id=COALESCE(account_id,(SELECT account_id FROM endpoints.proxy_endpoints WHERE tenant_id=$4 AND id=$5)),node_id=$7,runner_id=$1,valid_until=$2,last_reason_code='runner_running',updated_at=$3 WHERE tenant_id=$4 AND endpoint_id=$5 AND desired_generation=$6 AND (node_id=$7 OR node_id IS NULL)`, o.RunnerID, now.Add(5*time.Minute), now, tenantID, endpointID, o.Generation, o.NodeID); err != nil {
				return struct{}{}, mapError(err)
			}
		}
		if o.State == RunnerFailed || o.State == RunnerOrphaned {
			if _, err := tx.Exec(ctx, `UPDATE scheduler.endpoint_assignments SET state='blocked',health_state='unhealthy',retry_attempts=LEAST(retry_attempts+1,1000),cooldown_until=$1,last_reason_code=$2,updated_at=$3 WHERE tenant_id=$4 AND endpoint_id=$5 AND desired_generation=$6`, now.Add(retryDelay(o.Generation)), strings.TrimSpace(o.ReasonCode), now, tenantID, endpointID, o.Generation); err != nil {
				return struct{}{}, mapError(err)
			}
		}
		if o.State == RunnerRunning || o.State == RunnerStopped {
			if _, err := tx.Exec(ctx, `UPDATE operations.operations SET state='succeeded',progress_category='completed',result_code=$1,safe_message='',completed_at=$2,updated_at=$2 WHERE id=$3`, "runner_"+status, now, o.OperationID); err != nil {
				return struct{}{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM reconciler.work_items WHERE operation_id=$1`, o.OperationID); err != nil {
				return struct{}{}, mapError(err)
			}
		}
		if o.State == RunnerStopped {
			if _, err := tx.Exec(ctx, `UPDATE scheduler.endpoint_assignments SET state='unassigned',runner_id=NULL,valid_until=NULL,last_reason_code='runner_stopped',updated_at=$1 WHERE tenant_id=$2 AND endpoint_id=$3 AND runner_id=$4`, now, tenantID, endpointID, o.RunnerID); err != nil {
				return struct{}{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM reconciler.runner_desired_states WHERE runner_id=$1`, o.RunnerID); err != nil {
				return struct{}{}, mapError(err)
			}
			if reservationID != nil {
				if _, err := tx.Exec(ctx, `DELETE FROM accounts.account_capacity_reservations WHERE tenant_id=$1 AND id=$2`, tenantID, *reservationID); err != nil {
					return struct{}{}, mapError(err)
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM reconciler.finalizers WHERE tenant_id=$1 AND resource_type='endpoint' AND resource_id=$2 AND finalizer='runner.cleanup'`, tenantID, endpointID); err != nil {
				return struct{}{}, mapError(err)
			}
			var lifecycle string
			if err := tx.QueryRow(ctx, `SELECT lifecycle_state FROM endpoints.proxy_endpoints WHERE tenant_id=$1 AND id=$2`, tenantID, endpointID).Scan(&lifecycle); err != nil {
				return struct{}{}, mapError(err)
			}
			if lifecycle == "deleting" {
				if _, err := tx.Exec(ctx, `DELETE FROM endpoints.proxy_endpoints WHERE tenant_id=$1 AND id=$2`, tenantID, endpointID); err != nil {
					return struct{}{}, mapError(err)
				}
			}
		}
		details := map[string]any{"runner_id": o.RunnerID.String(), "endpoint_id": endpointID.String(), "node_id": o.NodeID.String(), "generation": o.Generation, "state": status, "reason_code": strings.TrimSpace(o.ReasonCode)}
		actorID := o.NodeID
		if err := s.audit.Append(ctx, tx, audit.Event{TenantID: &tenantID, ActorType: "node_agent", ActorID: &actorID, Action: "reconciler.runner.observed", ResourceType: "runner", ResourceID: &o.RunnerID, Result: "success", SourceIP: o.Metadata.SourceIP, UserAgent: o.Metadata.UserAgent, RequestID: o.Metadata.RequestID, Details: details}, audit.OutboxEvent{EventType: "reconciler.runner.observed", AggregateType: "runner", AggregateID: o.RunnerID, PayloadVersion: 1, Payload: details, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}
func validState(s RunnerState) bool {
	return s == RunnerRunning || s == RunnerStopped || s == RunnerFailed || s == RunnerOrphaned
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%w: %w", ErrStorage, err)
}
