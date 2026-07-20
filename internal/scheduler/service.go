package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrAssignmentForbidden = errors.New("assignment operation is forbidden")
	ErrAssignmentNotFound  = errors.New("assignment was not found")
	ErrAssignmentConflict  = errors.New("assignment version conflict")
	ErrPoolBindingRequired = errors.New("endpoint is not pool bound")
	ErrAssignmentStorage   = errors.New("assignment storage failure")
)

type AssignmentLeaser interface {
	Acquire(context.Context, []ResourceKey) ([]Lease, error)
	Release(context.Context, []Lease) error
}

type AssignmentService struct {
	pools  *database.Pools
	leaser AssignmentLeaser
	now    func() time.Time
	newID  func() (uuid.UUID, error)
}

func NewAssignmentService(pools *database.Pools, leaser AssignmentLeaser) (*AssignmentService, error) {
	if pools == nil || pools.Tenant == nil || pools.Platform == nil || leaser == nil {
		return nil, ErrSchedulerInvalid
	}
	return &AssignmentService{pools: pools, leaser: leaser, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewV7}, nil
}

type Assignment struct {
	TenantID          uuid.UUID  `json:"tenant_id"`
	EndpointID        uuid.UUID  `json:"endpoint_id"`
	AssignmentID      uuid.UUID  `json:"assignment_id"`
	PoolID            *uuid.UUID `json:"pool_id,omitempty"`
	AccountID         *uuid.UUID `json:"account_id,omitempty"`
	NodeID            *uuid.UUID `json:"node_id,omitempty"`
	RunnerID          *uuid.UUID `json:"runner_id,omitempty"`
	DesiredGeneration int64      `json:"desired_generation"`
	FencingToken      int64      `json:"fencing_token"`
	Strategy          string     `json:"strategy"`
	State             string     `json:"state"`
	HealthState       string     `json:"health_state"`
	RetryAttempts     int32      `json:"retry_attempts"`
	CooldownUntil     *time.Time `json:"cooldown_until,omitempty"`
	ValidUntil        *time.Time `json:"valid_until,omitempty"`
	LastReasonCode    string     `json:"last_reason_code"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
type ReconcileCommand struct {
	EndpointID              uuid.UUID
	ExpectedEndpointVersion int64
	ReasonCode              string
}

func (command ReconcileCommand) Validate() error {
	if command.EndpointID == uuid.Nil || command.ExpectedEndpointVersion < 1 || len(command.ReasonCode) < 1 || len(command.ReasonCode) > 128 {
		return ErrSchedulerInvalid
	}
	return nil
}

func (service *AssignmentService) Get(ctx context.Context, actor tenancy.TenantActor, endpointID uuid.UUID) (Assignment, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return Assignment{}, ErrAssignmentForbidden
	}
	if endpointID == uuid.Nil {
		return Assignment{}, ErrSchedulerInvalid
	}
	return database.InTenantTx(ctx, service.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Assignment, error) {
		return scanAssignment(tx.QueryRow(ctx, `SELECT tenant_id,endpoint_id,assignment_id,pool_id,account_id,node_id,runner_id,desired_generation,fencing_token,strategy,state,health_state,retry_attempts,cooldown_until,valid_until,last_reason_code,created_at,updated_at FROM scheduler.endpoint_assignments WHERE tenant_id=$1 AND endpoint_id=$2`, actor.TenantID(), endpointID))
	})
}

func (service *AssignmentService) Reconcile(ctx context.Context, actor tenancy.TenantActor, command ReconcileCommand) (Assignment, error) {
	if !actor.Allows(tenancy.ActionOperateResources) {
		return Assignment{}, ErrAssignmentForbidden
	}
	if command.Validate() != nil {
		return Assignment{}, ErrSchedulerInvalid
	}
	var poolID uuid.UUID
	var binding string
	if err := service.pools.Platform.QueryRow(ctx, `SELECT binding_mode,pool_id FROM endpoints.proxy_endpoints WHERE tenant_id=$1 AND id=$2`, actor.TenantID(), command.EndpointID).Scan(&binding, &poolID); err != nil {
		return Assignment{}, mapAssignmentError(err)
	}
	if binding != "pool" || poolID == uuid.Nil {
		return Assignment{}, ErrPoolBindingRequired
	}
	leases, err := service.leaser.Acquire(ctx, []ResourceKey{{Kind: "endpoint", TenantID: actor.TenantID(), ResourceID: command.EndpointID}, {Kind: "pool", TenantID: actor.TenantID(), ResourceID: poolID}})
	if err != nil {
		return Assignment{}, err
	}
	defer service.leaser.Release(context.WithoutCancel(ctx), leases)
	var fencing uint64
	for _, lease := range leases {
		if lease.FencingToken > fencing {
			fencing = lease.FencingToken
		}
	}
	operationID, err := service.newID()
	if err != nil {
		return Assignment{}, ErrAssignmentStorage
	}
	workID, err := service.newID()
	if err != nil {
		return Assignment{}, ErrAssignmentStorage
	}
	now := service.now().UTC()
	return database.InPlatformTx(ctx, service.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Assignment, error) {
		var version, generation int64
		if err := tx.QueryRow(ctx, `SELECT version,desired_generation FROM endpoints.proxy_endpoints WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, actor.TenantID(), command.EndpointID).Scan(&version, &generation); err != nil {
			return Assignment{}, mapAssignmentError(err)
		}
		if version != command.ExpectedEndpointVersion {
			return Assignment{}, ErrAssignmentConflict
		}
		generation++
		assignment, err := scanAssignment(tx.QueryRow(ctx, `UPDATE scheduler.endpoint_assignments SET desired_generation=$3,fencing_token=$4,state='acquiring',retry_attempts=0,cooldown_until=NULL,last_reason_code=$5,updated_at=$6 WHERE tenant_id=$1 AND endpoint_id=$2 AND fencing_token <= $4 RETURNING tenant_id,endpoint_id,assignment_id,pool_id,account_id,node_id,runner_id,desired_generation,fencing_token,strategy,state,health_state,retry_attempts,cooldown_until,valid_until,last_reason_code,created_at,updated_at`, actor.TenantID(), command.EndpointID, generation, int64(fencing), command.ReasonCode, now))
		if err != nil {
			return Assignment{}, mapAssignmentError(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE endpoints.proxy_endpoints SET desired_generation=$1,version=version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4`, generation, now, actor.TenantID(), command.EndpointID); err != nil {
			return Assignment{}, mapAssignmentError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operations.operations (id,tenant_id,operation_type,resource_type,resource_id,requested_generation,state,progress_category,requested_by,created_at,updated_at) VALUES ($1,$2,'endpoint.assignment.reconcile','endpoint',$3,$4,'queued','queued',$5,$6,$6)`, operationID, actor.TenantID(), command.EndpointID, generation, actor.ActorID(), now); err != nil {
			return Assignment{}, mapAssignmentError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reconciler.work_items (id,tenant_id,resource_type,resource_id,action,generation,operation_id,available_at,created_at,updated_at) VALUES ($1,$2,'endpoint',$3,'schedule',$4,$5,$6,$6,$6)`, workID, actor.TenantID(), command.EndpointID, generation, operationID, now); err != nil {
			return Assignment{}, mapAssignmentError(err)
		}
		return assignment, nil
	})
}

type assignmentScanner interface{ Scan(...any) error }

func scanAssignment(row assignmentScanner) (Assignment, error) {
	var value Assignment
	err := row.Scan(&value.TenantID, &value.EndpointID, &value.AssignmentID, &value.PoolID, &value.AccountID, &value.NodeID, &value.RunnerID, &value.DesiredGeneration, &value.FencingToken, &value.Strategy, &value.State, &value.HealthState, &value.RetryAttempts, &value.CooldownUntil, &value.ValidUntil, &value.LastReasonCode, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return Assignment{}, mapAssignmentError(err)
	}
	return value, nil
}
func mapAssignmentError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAssignmentNotFound
	}
	return ErrAssignmentStorage
}
