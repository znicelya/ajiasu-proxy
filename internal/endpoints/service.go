package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	pools *database.Pools
	audit audit.Service
	now   func() time.Time
	newID func() (uuid.UUID, error)
}

func NewService(pools *database.Pools, auditService audit.Service) (*Service, error) {
	if pools == nil || pools.Tenant == nil {
		return nil, ErrInvalidArgument
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &Service{pools: pools, audit: auditService, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewV7}, nil
}

func (s *Service) Create(ctx context.Context, actor tenancy.TenantActor, cmd CreateCommand) (Endpoint, Operation, error) {
	if !actor.Allows(tenancy.ActionManageResources) {
		return Endpoint{}, Operation{}, ErrForbidden
	}
	if cmd.DesiredRunnerState == "" {
		cmd.DesiredRunnerState = DesiredRunning
	}
	if cmd.BindingMode == "" {
		cmd.BindingMode = "fixed"
	}
	if cmd.Validate() != nil {
		return Endpoint{}, Operation{}, ErrInvalidArgument
	}
	endpointID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	operationID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	runnerID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	reservationID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	workID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	assignmentID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	type result struct {
		endpoint  Endpoint
		operation Operation
	}
	out, err := database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (result, error) {
		now := s.now().UTC()
		if cmd.BindingMode == "pool" {
			var poolState, strategy string
			if err := tx.QueryRow(ctx, `SELECT state,strategy FROM pools.account_pools WHERE tenant_id=$1 AND id=$2`, actor.TenantID(), cmd.PoolID).Scan(&poolState, &strategy); err != nil {
				return result{}, mapError(err)
			}
			if poolState != "active" {
				return result{}, ErrInvalidArgument
			}
			row := tx.QueryRow(ctx, `INSERT INTO endpoints.proxy_endpoints (id,tenant_id,name,normalized_name,binding_mode,pool_id,desired_runner_state,created_at,updated_at) VALUES ($1,$2,$3,$4,'pool',$5,$6,$7,$7) RETURNING id,tenant_id,name,binding_mode,account_id,node_id,pool_id,desired_runner_state,lifecycle_state,desired_generation,version,created_at,updated_at`, endpointID, actor.TenantID(), strings.TrimSpace(cmd.Name), strings.ToLower(strings.TrimSpace(cmd.Name)), cmd.PoolID, cmd.DesiredRunnerState, now)
			endpoint, err := scanEndpoint(row)
			if err != nil {
				return result{}, mapError(err)
			}
			statusState, reason, operationState, progress := ObservedStopped, "desired_stopped", "succeeded", "completed"
			if cmd.DesiredRunnerState == DesiredRunning {
				statusState, reason, operationState, progress = ObservedPending, "awaiting_assignment", "queued", "queued"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO endpoints.endpoint_status (tenant_id,endpoint_id,observed_state,reason_code,last_transition_at,updated_at) VALUES ($1,$2,$3,$4,$5,$5)`, actor.TenantID(), endpointID, statusState, reason, now); err != nil {
				return result{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO scheduler.endpoint_assignments (tenant_id,endpoint_id,assignment_id,pool_id,desired_generation,fencing_token,strategy,state,created_at,updated_at) VALUES ($1,$2,$3,$4,1,0,$5,'unassigned',$6,$6)`, actor.TenantID(), endpointID, assignmentID, cmd.PoolID, strategy, now); err != nil {
				return result{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO operations.operations (id,tenant_id,operation_type,resource_type,resource_id,requested_generation,state,progress_category,requested_by,created_at,updated_at,completed_at) VALUES ($1,$2,'endpoint.create','endpoint',$3,1,$4,$5,$6,$7,$7,CASE WHEN $4='succeeded' THEN $7 ELSE NULL END)`, operationID, actor.TenantID(), endpointID, operationState, progress, actor.ActorID(), now); err != nil {
				return result{}, mapError(err)
			}
			if cmd.DesiredRunnerState == DesiredRunning {
				if _, err := tx.Exec(ctx, `INSERT INTO reconciler.work_items (id,tenant_id,resource_type,resource_id,action,generation,operation_id,available_at,created_at,updated_at) VALUES ($1,$2,'endpoint',$3,'schedule',1,$4,$5,$5,$5)`, workID, actor.TenantID(), endpointID, operationID, now); err != nil {
					return result{}, mapError(err)
				}
			}
			endpoint.Status.ObservedState, endpoint.Status.ReasonCode, endpoint.Status.LastTransitionAt = statusState, reason, now
			operation := Operation{ID: operationID, TenantID: actor.TenantID(), OperationType: "endpoint.create", ResourceType: "endpoint", ResourceID: endpointID, RequestedGeneration: 1, State: operationState, ProgressCategory: progress, CreatedAt: now, UpdatedAt: now}
			if err := s.appendAudit(ctx, tx, actor, "endpoints.endpoint.created", endpointID, map[string]any{"endpoint_id": endpointID.String(), "pool_id": cmd.PoolID.String(), "binding_mode": "pool", "generation": int64(1)}, now); err != nil {
				return result{}, err
			}
			return result{endpoint: endpoint, operation: operation}, nil
		}
		var maintenance, connectivity string
		var maxRunners, reservedHeadroom, activeRunners int
		if err := tx.QueryRow(ctx, `SELECT maintenance_state,connectivity_state,max_runners,reserved_headroom,active_runners FROM nodes.nodes WHERE id=$1`, cmd.NodeID).Scan(&maintenance, &connectivity, &maxRunners, &reservedHeadroom, &activeRunners); err != nil {
			return result{}, mapError(err)
		}
		if maintenance != "active" || connectivity == "offline" {
			return result{}, ErrNodeUnavailable
		}
		if activeRunners >= maxRunners-reservedHeadroom {
			return result{}, ErrNodeCapacity
		}
		var accountState string
		var maxConcurrency int
		var credentialVersion int64
		if err := tx.QueryRow(ctx, `SELECT a.state,a.max_concurrency,c.version FROM accounts.accounts a JOIN accounts.account_credentials c ON c.tenant_id=a.tenant_id AND c.account_id=a.id AND c.retired_at IS NULL WHERE a.tenant_id=$1 AND a.id=$2 FOR UPDATE OF a`, actor.TenantID(), cmd.AccountID).Scan(&accountState, &maxConcurrency, &credentialVersion); err != nil {
			return result{}, mapError(err)
		}
		if accountState != "active" {
			return result{}, ErrInvalidArgument
		}
		var reservations int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts.account_capacity_reservations WHERE tenant_id=$1 AND account_id=$2 AND expires_at>$3`, actor.TenantID(), cmd.AccountID, now).Scan(&reservations); err != nil {
			return result{}, mapError(err)
		}
		if cmd.DesiredRunnerState == DesiredRunning && reservations >= maxConcurrency {
			return result{}, ErrAccountCapacity
		}
		row := tx.QueryRow(ctx, `INSERT INTO endpoints.proxy_endpoints (id,tenant_id,name,normalized_name,account_id,node_id,desired_runner_state,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8) RETURNING id,tenant_id,name,binding_mode,account_id,node_id,pool_id,desired_runner_state,lifecycle_state,desired_generation,version,created_at,updated_at`, endpointID, actor.TenantID(), strings.TrimSpace(cmd.Name), strings.ToLower(strings.TrimSpace(cmd.Name)), cmd.AccountID, cmd.NodeID, cmd.DesiredRunnerState, now)
		endpoint, err := scanEndpoint(row)
		if err != nil {
			return result{}, mapError(err)
		}
		statusState := ObservedStopped
		reason := "desired_stopped"
		operationState := "succeeded"
		progress := "completed"
		if cmd.DesiredRunnerState == DesiredRunning {
			statusState = ObservedPending
			reason = "awaiting_reconciliation"
			operationState = "queued"
			progress = "queued"
		}
		var statusRunner any
		if cmd.DesiredRunnerState == DesiredRunning {
			statusRunner = runnerID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO endpoints.endpoint_status (tenant_id,endpoint_id,observed_state,runner_id,reason_code,last_transition_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$6)`, actor.TenantID(), endpointID, statusState, statusRunner, reason, now); err != nil {
			return result{}, mapError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO scheduler.endpoint_assignments (tenant_id,endpoint_id,assignment_id,desired_generation,state,created_at,updated_at) VALUES ($1,$2,$3,1,'unassigned',$4,$4)`, actor.TenantID(), endpointID, assignmentID, now); err != nil {
			return result{}, mapError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operations.operations (id,tenant_id,operation_type,resource_type,resource_id,requested_generation,state,progress_category,requested_by,created_at,updated_at,completed_at) VALUES ($1,$2,'endpoint.create','endpoint',$3,1,$4,$5,$6,$7::timestamptz,$7::timestamptz,CASE WHEN $4='succeeded' THEN $7::timestamptz ELSE NULL END)`, operationID, actor.TenantID(), endpointID, operationState, progress, actor.ActorID(), now); err != nil {
			return result{}, mapError(err)
		}
		if cmd.DesiredRunnerState == DesiredRunning {
			expires := now.Add(24 * time.Hour)
			if _, err := tx.Exec(ctx, `INSERT INTO accounts.account_capacity_reservations (id,tenant_id,account_id,owner_id,created_at,expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, reservationID, actor.TenantID(), cmd.AccountID, endpointID, now, expires); err != nil {
				return result{}, mapError(err)
			}
			runtimeSpec, _ := json.Marshal(map[string]any{"runner_image": "phase1-reviewed", "network_mode": "isolated", "read_only": true})
			if _, err := tx.Exec(ctx, `INSERT INTO reconciler.runner_desired_states (tenant_id,endpoint_id,runner_id,node_id,account_id,credential_version,desired_generation,desired_action,operation_id,capacity_reservation_id,runtime_spec,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,1,'create',$7,$8,$9,$10,$10)`, actor.TenantID(), endpointID, runnerID, cmd.NodeID, cmd.AccountID, credentialVersion, operationID, reservationID, runtimeSpec, now); err != nil {
				return result{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO reconciler.work_items (id,tenant_id,resource_type,resource_id,action,generation,operation_id,available_at,created_at,updated_at) VALUES ($1,$2,'endpoint',$3,'create',1,$4,$5,$5,$5)`, workID, actor.TenantID(), endpointID, operationID, now); err != nil {
				return result{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO reconciler.finalizers (tenant_id,resource_type,resource_id,finalizer,created_at) VALUES ($1,'endpoint',$2,'runner.cleanup',$3)`, actor.TenantID(), endpointID, now); err != nil {
				return result{}, mapError(err)
			}
			endpoint.Status.RunnerID = &runnerID
		}
		endpoint.Status.ObservedState = statusState
		endpoint.Status.ReasonCode = reason
		endpoint.Status.LastTransitionAt = now
		operation := Operation{ID: operationID, TenantID: actor.TenantID(), OperationType: "endpoint.create", ResourceType: "endpoint", ResourceID: endpointID, RequestedGeneration: 1, State: operationState, ProgressCategory: progress, CreatedAt: now, UpdatedAt: now}
		if err := s.appendAudit(ctx, tx, actor, "endpoints.endpoint.created", endpointID, map[string]any{"endpoint_id": endpointID.String(), "account_id": cmd.AccountID.String(), "node_id": cmd.NodeID.String(), "generation": int64(1)}, now); err != nil {
			return result{}, err
		}
		return result{endpoint: endpoint, operation: operation}, nil
	})
	if err != nil {
		return Endpoint{}, Operation{}, err
	}
	return out.endpoint, out.operation, nil
}

func (s *Service) Get(ctx context.Context, actor tenancy.TenantActor, endpointID uuid.UUID) (Endpoint, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return Endpoint{}, ErrForbidden
	}
	if endpointID == uuid.Nil {
		return Endpoint{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Endpoint, error) {
		return getEndpoint(ctx, tx, actor.TenantID(), endpointID)
	})
}

func (s *Service) List(ctx context.Context, actor tenancy.TenantActor, after time.Time, afterID uuid.UUID, limit int32) ([]Endpoint, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 200 {
		return nil, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]Endpoint, error) {
		rows, err := tx.Query(ctx, `SELECT e.id,e.tenant_id,e.name,e.binding_mode,e.account_id,e.node_id,e.pool_id,e.desired_runner_state,e.lifecycle_state,e.desired_generation,e.version,e.created_at,e.updated_at,s.observed_generation,s.observed_state,s.runner_id,s.reason_code,s.last_agent_observation_at,s.last_transition_at FROM endpoints.proxy_endpoints e JOIN endpoints.endpoint_status s ON s.tenant_id=e.tenant_id AND s.endpoint_id=e.id WHERE e.tenant_id=$1 AND (e.created_at,e.id)>($2,$3) ORDER BY e.created_at,e.id LIMIT $4`, actor.TenantID(), after, afterID, limit)
		if err != nil {
			return nil, mapError(err)
		}
		defer rows.Close()
		items := make([]Endpoint, 0)
		for rows.Next() {
			item, err := scanEndpointWithStatus(rows)
			if err != nil {
				return nil, mapError(err)
			}
			items = append(items, item)
		}
		return items, mapError(rows.Err())
	})
}

func (s *Service) RequestDelete(ctx context.Context, actor tenancy.TenantActor, endpointID uuid.UUID, expectedVersion int64) (Endpoint, Operation, error) {
	if !actor.Allows(tenancy.ActionManageResources) {
		return Endpoint{}, Operation{}, ErrForbidden
	}
	if endpointID == uuid.Nil || expectedVersion < 1 {
		return Endpoint{}, Operation{}, ErrInvalidArgument
	}
	operationID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	workID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	type result struct {
		endpoint  Endpoint
		operation Operation
	}
	out, err := database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (result, error) {
		current, err := getEndpoint(ctx, tx, actor.TenantID(), endpointID)
		if err != nil {
			return result{}, err
		}
		if current.Version != expectedVersion || current.LifecycleState == LifecycleDeleting {
			return result{}, ErrVersionConflict
		}
		now := s.now().UTC()
		generation := current.DesiredGeneration + 1
		row := tx.QueryRow(ctx, `UPDATE endpoints.proxy_endpoints SET lifecycle_state='deleting',desired_runner_state='stopped',desired_generation=$1,version=version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 RETURNING id,tenant_id,name,binding_mode,account_id,node_id,pool_id,desired_runner_state,lifecycle_state,desired_generation,version,created_at,updated_at`, generation, now, actor.TenantID(), endpointID, expectedVersion)
		updated, err := scanEndpoint(row)
		if err != nil {
			return result{}, mapError(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO operations.operations (id,tenant_id,operation_type,resource_type,resource_id,requested_generation,state,progress_category,requested_by,created_at,updated_at) VALUES ($1,$2,'endpoint.delete','endpoint',$3,$4,'queued','queued',$5,$6,$6)`, operationID, actor.TenantID(), endpointID, generation, actor.ActorID(), now); err != nil {
			return result{}, mapError(err)
		}
		tag, err := tx.Exec(ctx, `UPDATE reconciler.runner_desired_states SET desired_generation=$1,desired_action='stop',operation_id=$2,updated_at=$3 WHERE tenant_id=$4 AND endpoint_id=$5`, generation, operationID, now, actor.TenantID(), endpointID)
		if err != nil {
			return result{}, mapError(err)
		}
		if tag.RowsAffected() == 0 {
			if _, err = tx.Exec(ctx, `UPDATE operations.operations SET state='succeeded',progress_category='completed',result_code='already_stopped',completed_at=$1,updated_at=$1 WHERE id=$2`, now, operationID); err != nil {
				return result{}, mapError(err)
			}
			if _, err = tx.Exec(ctx, `DELETE FROM endpoints.proxy_endpoints WHERE tenant_id=$1 AND id=$2`, actor.TenantID(), endpointID); err != nil {
				return result{}, mapError(err)
			}
			operation := Operation{ID: operationID, TenantID: actor.TenantID(), OperationType: "endpoint.delete", ResourceType: "endpoint", ResourceID: endpointID, RequestedGeneration: generation, State: "succeeded", ProgressCategory: "completed", CreatedAt: now, UpdatedAt: now}
			return result{endpoint: updated, operation: operation}, nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO reconciler.work_items (id,tenant_id,resource_type,resource_id,action,generation,operation_id,available_at,created_at,updated_at) VALUES ($1,$2,'endpoint',$3,'stop',$4,$5,$6,$6,$6)`, workID, actor.TenantID(), endpointID, generation, operationID, now); err != nil {
			return result{}, mapError(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE endpoints.endpoint_status SET observed_state='stopping',reason_code='deletion_requested',last_transition_at=$1,updated_at=$1 WHERE tenant_id=$2 AND endpoint_id=$3`, now, actor.TenantID(), endpointID); err != nil {
			return result{}, mapError(err)
		}
		updated.Status = current.Status
		updated.Status.ObservedState = ObservedStopping
		updated.Status.ReasonCode = "deletion_requested"
		updated.Status.LastTransitionAt = now
		operation := Operation{ID: operationID, TenantID: actor.TenantID(), OperationType: "endpoint.delete", ResourceType: "endpoint", ResourceID: endpointID, RequestedGeneration: generation, State: "queued", ProgressCategory: "queued", CreatedAt: now, UpdatedAt: now}
		if err = s.appendAudit(ctx, tx, actor, "endpoints.endpoint.deletion_requested", endpointID, map[string]any{"endpoint_id": endpointID.String(), "generation": generation}, now); err != nil {
			return result{}, err
		}
		return result{endpoint: updated, operation: operation}, nil
	})
	if err != nil {
		return Endpoint{}, Operation{}, err
	}
	return out.endpoint, out.operation, nil
}

// RequestState queues a start, stop, or rebuild operation without waiting for the Agent.
func (s *Service) RequestState(ctx context.Context, actor tenancy.TenantActor, endpointID uuid.UUID, expectedVersion int64, action string) (Endpoint, Operation, error) {
	if !actor.Allows(tenancy.ActionOperateResources) {
		return Endpoint{}, Operation{}, ErrForbidden
	}
	if endpointID == uuid.Nil || expectedVersion < 1 || (action != "start" && action != "stop" && action != "rebuild") {
		return Endpoint{}, Operation{}, ErrInvalidArgument
	}
	operationID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	workID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	runnerID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	reservationID, err := s.newID()
	if err != nil {
		return Endpoint{}, Operation{}, ErrStorage
	}
	type result struct {
		endpoint  Endpoint
		operation Operation
	}
	out, err := database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (result, error) {
		current, err := getEndpoint(ctx, tx, actor.TenantID(), endpointID)
		if err != nil {
			return result{}, err
		}
		if current.Version != expectedVersion || current.LifecycleState != LifecycleActive {
			return result{}, ErrVersionConflict
		}
		if current.BindingMode == "pool" {
			return result{}, ErrPoolAssignmentRequired
		}
		if action == "start" && current.DesiredRunnerState == DesiredRunning {
			return result{}, ErrInvalidArgument
		}
		if action == "stop" && current.DesiredRunnerState == DesiredStopped {
			return result{}, ErrInvalidArgument
		}
		if action == "rebuild" && current.DesiredRunnerState != DesiredRunning {
			return result{}, ErrInvalidArgument
		}
		now := s.now().UTC()
		generation := current.DesiredGeneration + 1
		operationType := "endpoint." + action
		if _, err := tx.Exec(ctx, `INSERT INTO operations.operations (id,tenant_id,operation_type,resource_type,resource_id,requested_generation,state,progress_category,requested_by,created_at,updated_at) VALUES ($1,$2,$3,'endpoint',$4,$5,'queued','queued',$6,$7,$7)`, operationID, actor.TenantID(), operationType, endpointID, generation, actor.ActorID(), now); err != nil {
			return result{}, mapError(err)
		}
		var updated Endpoint
		if action == "start" {
			if err := reserveForStart(ctx, tx, actor.TenantID(), current.AccountID, current.NodeID, endpointID, reservationID, now); err != nil {
				return result{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE endpoints.proxy_endpoints SET desired_runner_state='running',desired_generation=$1,version=version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5`, generation, now, actor.TenantID(), endpointID, expectedVersion); err != nil {
				return result{}, mapError(err)
			}
			updated, err = getEndpoint(ctx, tx, actor.TenantID(), endpointID)
			if err != nil {
				return result{}, err
			}
			var credentialVersion int64
			if err := tx.QueryRow(ctx, `SELECT version FROM accounts.account_credentials WHERE tenant_id=$1 AND account_id=$2 AND retired_at IS NULL`, actor.TenantID(), current.AccountID).Scan(&credentialVersion); err != nil {
				return result{}, mapError(err)
			}
			runtimeSpec, _ := json.Marshal(map[string]any{"runner_image": "phase1-reviewed", "network_mode": "isolated", "read_only": true})
			if _, err := tx.Exec(ctx, `INSERT INTO reconciler.runner_desired_states (tenant_id,endpoint_id,runner_id,node_id,account_id,credential_version,desired_generation,desired_action,operation_id,capacity_reservation_id,runtime_spec,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'create',$8,$9,$10,$11,$11)`, actor.TenantID(), endpointID, runnerID, current.NodeID, current.AccountID, credentialVersion, generation, operationID, reservationID, runtimeSpec, now); err != nil {
				return result{}, mapError(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO reconciler.finalizers (tenant_id,resource_type,resource_id,finalizer,created_at) VALUES ($1,'endpoint',$2,'runner.cleanup',$3) ON CONFLICT DO NOTHING`, actor.TenantID(), endpointID, now); err != nil {
				return result{}, mapError(err)
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE endpoints.proxy_endpoints SET desired_runner_state=$1,desired_generation=$2,version=version+1,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6`, func() string {
				if action == "stop" {
					return "stopped"
				}
				return "running"
			}(), generation, now, actor.TenantID(), endpointID, expectedVersion); err != nil {
				return result{}, mapError(err)
			}
			updated, err = getEndpoint(ctx, tx, actor.TenantID(), endpointID)
			if err != nil {
				return result{}, err
			}
			if action == "stop" {
				if _, err := tx.Exec(ctx, `UPDATE reconciler.runner_desired_states SET desired_generation=$1,desired_action='stop',operation_id=$2,updated_at=$3 WHERE tenant_id=$4 AND endpoint_id=$5`, generation, operationID, now, actor.TenantID(), endpointID); err != nil {
					return result{}, mapError(err)
				}
			} else {
				if _, err := tx.Exec(ctx, `UPDATE reconciler.runner_desired_states SET desired_generation=$1,desired_action='rebuild',operation_id=$2,updated_at=$3 WHERE tenant_id=$4 AND endpoint_id=$5`, generation, operationID, now, actor.TenantID(), endpointID); err != nil {
					return result{}, mapError(err)
				}
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reconciler.work_items (id,tenant_id,resource_type,resource_id,action,generation,operation_id,available_at,created_at,updated_at) VALUES ($1,$2,'endpoint',$3,$4,$5,$6,$7,$7,$7)`, workID, actor.TenantID(), endpointID, action, generation, operationID, now); err != nil {
			return result{}, mapError(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE endpoints.endpoint_status SET observed_state=$1,runner_id=CASE WHEN $2='start' THEN $3 ELSE runner_id END,reason_code=$4,last_transition_at=$5,updated_at=$5 WHERE tenant_id=$6 AND endpoint_id=$7`, func() string {
			if action == "stop" {
				return string(ObservedStopping)
			}
			return string(ObservedStarting)
		}(), action, runnerID, action+"_requested", now, actor.TenantID(), endpointID); err != nil {
			return result{}, mapError(err)
		}
		updated.Status.ObservedState = map[string]ObservedState{"stop": ObservedStopping}[action]
		if updated.Status.ObservedState == "" {
			updated.Status.ObservedState = ObservedStarting
		}
		updated.Status.ReasonCode = action + "_requested"
		updated.Status.LastTransitionAt = now
		return result{endpoint: updated, operation: Operation{ID: operationID, TenantID: actor.TenantID(), OperationType: operationType, ResourceType: "endpoint", ResourceID: endpointID, RequestedGeneration: generation, State: "queued", ProgressCategory: "queued", CreatedAt: now, UpdatedAt: now}}, nil
	})
	if err != nil {
		return Endpoint{}, Operation{}, err
	}
	return out.endpoint, out.operation, nil
}

func reserveForStart(ctx context.Context, tx pgx.Tx, tenantID, accountID, nodeID, ownerID, reservationID uuid.UUID, now time.Time) error {
	var maintenance, connectivity string
	var maxRunners, headroom, active int
	if err := tx.QueryRow(ctx, `SELECT maintenance_state,connectivity_state,max_runners,reserved_headroom,active_runners FROM nodes.nodes WHERE id=$1`, nodeID).Scan(&maintenance, &connectivity, &maxRunners, &headroom, &active); err != nil {
		return mapError(err)
	}
	if maintenance != "active" || connectivity != "online" || active >= maxRunners-headroom {
		return ErrNodeUnavailable
	}
	var state string
	var maxConcurrency, version int
	if err := tx.QueryRow(ctx, `SELECT a.state,a.max_concurrency,c.version FROM accounts.accounts a JOIN accounts.account_credentials c ON c.tenant_id=a.tenant_id AND c.account_id=a.id AND c.retired_at IS NULL WHERE a.tenant_id=$1 AND a.id=$2 FOR UPDATE OF a`, tenantID, accountID).Scan(&state, &maxConcurrency, &version); err != nil {
		return mapError(err)
	}
	if state != "active" {
		return ErrInvalidArgument
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts.account_capacity_reservations WHERE tenant_id=$1 AND account_id=$2 AND expires_at>$3`, tenantID, accountID, now).Scan(&count); err != nil {
		return mapError(err)
	}
	if count >= maxConcurrency {
		return ErrAccountCapacity
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounts.account_capacity_reservations (id,tenant_id,account_id,owner_id,created_at,expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, reservationID, tenantID, accountID, ownerID, now, now.Add(24*time.Hour)); err != nil {
		return mapError(err)
	}
	return nil
}

func getEndpoint(ctx context.Context, tx pgx.Tx, tenantID, endpointID uuid.UUID) (Endpoint, error) {
	row := tx.QueryRow(ctx, `SELECT e.id,e.tenant_id,e.name,e.binding_mode,e.account_id,e.node_id,e.pool_id,e.desired_runner_state,e.lifecycle_state,e.desired_generation,e.version,e.created_at,e.updated_at,s.observed_generation,s.observed_state,s.runner_id,s.reason_code,s.last_agent_observation_at,s.last_transition_at FROM endpoints.proxy_endpoints e JOIN endpoints.endpoint_status s ON s.tenant_id=e.tenant_id AND s.endpoint_id=e.id WHERE e.tenant_id=$1 AND e.id=$2`, tenantID, endpointID)
	item, err := scanEndpointWithStatus(row)
	return item, mapError(err)
}

type scanner interface{ Scan(...any) error }

func scanEndpoint(row scanner) (Endpoint, error) {
	var e Endpoint
	var accountID, nodeID *uuid.UUID
	var desired, lifecycle string
	if err := row.Scan(&e.ID, &e.TenantID, &e.Name, &e.BindingMode, &accountID, &nodeID, &e.PoolID, &desired, &lifecycle, &e.DesiredGeneration, &e.Version, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return Endpoint{}, err
	}
	if accountID != nil {
		e.AccountID = *accountID
	}
	if nodeID != nil {
		e.NodeID = *nodeID
	}
	e.DesiredRunnerState = DesiredRunnerState(desired)
	e.LifecycleState = LifecycleState(lifecycle)
	return e, nil
}
func scanEndpointWithStatus(row scanner) (Endpoint, error) {
	var e Endpoint
	var accountID, nodeID *uuid.UUID
	var desired, lifecycle, observed string
	if err := row.Scan(&e.ID, &e.TenantID, &e.Name, &e.BindingMode, &accountID, &nodeID, &e.PoolID, &desired, &lifecycle, &e.DesiredGeneration, &e.Version, &e.CreatedAt, &e.UpdatedAt, &e.Status.ObservedGeneration, &observed, &e.Status.RunnerID, &e.Status.ReasonCode, &e.Status.LastAgentObservationAt, &e.Status.LastTransitionAt); err != nil {
		return Endpoint{}, err
	}
	if accountID != nil {
		e.AccountID = *accountID
	}
	if nodeID != nil {
		e.NodeID = *nodeID
	}
	e.DesiredRunnerState = DesiredRunnerState(desired)
	e.LifecycleState = LifecycleState(lifecycle)
	e.Status.ObservedState = ObservedState(observed)
	return e, nil
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		switch pgErr.SQLState() {
		case "23505":
			return ErrAlreadyExists
		case "23503", "23514", "22P02":
			return ErrInvalidArgument
		}
	}
	return fmt.Errorf("%w: %w", ErrStorage, err)
}
func (s *Service) appendAudit(ctx context.Context, tx pgx.Tx, actor tenancy.TenantActor, action string, resourceID uuid.UUID, details map[string]any, now time.Time) error {
	metadata := actor.Metadata()
	tenantID := actor.TenantID()
	actorID := actor.ActorID()
	return s.audit.Append(ctx, tx, audit.Event{TenantID: &tenantID, ActorType: metadata.ActorType, ActorID: &actorID, Action: action, ResourceType: "endpoint", ResourceID: &resourceID, Result: "success", SourceIP: metadata.SourceIP, UserAgent: metadata.UserAgent, RequestID: metadata.RequestID, Details: details}, audit.OutboxEvent{EventType: action, AggregateType: "endpoint", AggregateID: resourceID, PayloadVersion: 1, Payload: details, AvailableAt: now})
}
