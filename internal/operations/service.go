package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct{ pools *database.Pools }

func NewService(pools *database.Pools) (*Service, error) {
	if pools == nil || pools.Tenant == nil || pools.Platform == nil {
		return nil, ErrInvalidArgument
	}
	return &Service{pools: pools}, nil
}

func (s *Service) GetTenant(ctx context.Context, actor tenancy.TenantActor, id uuid.UUID) (Operation, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return Operation{}, ErrForbidden
	}
	if id == uuid.Nil {
		return Operation{}, ErrInvalidArgument
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Operation, error) {
		return scanOperation(tx.QueryRow(ctx, operationSelect+` WHERE id=$1 AND tenant_id=$2`, id, actor.TenantID()))
	})
}

func (s *Service) ListTenant(ctx context.Context, actor tenancy.TenantActor, before time.Time, beforeID uuid.UUID, limit int32) ([]Operation, error) {
	if !actor.Allows(tenancy.ActionReadResources) {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 200 {
		return nil, ErrInvalidArgument
	}
	before, beforeID = normalizeBefore(before, beforeID)
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]Operation, error) {
		return scanOperations(tx.Query(ctx, operationSelect+` WHERE tenant_id=$1 AND (created_at,id)<($2,$3) ORDER BY created_at DESC,id DESC LIMIT $4`, actor.TenantID(), before, beforeID, limit))
	})
}

func (s *Service) GetPlatform(ctx context.Context, actor tenancy.PlatformActor, id uuid.UUID) (Operation, error) {
	if !actor.Allows(tenancy.ActionReadPlatformOps) {
		return Operation{}, ErrForbidden
	}
	if id == uuid.Nil {
		return Operation{}, ErrInvalidArgument
	}
	return database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (Operation, error) {
		return scanOperation(tx.QueryRow(ctx, operationSelect+` WHERE id=$1`, id))
	})
}

func (s *Service) ListPlatform(ctx context.Context, actor tenancy.PlatformActor, before time.Time, beforeID uuid.UUID, limit int32) ([]Operation, error) {
	if !actor.Allows(tenancy.ActionReadPlatformOps) {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 200 {
		return nil, ErrInvalidArgument
	}
	before, beforeID = normalizeBefore(before, beforeID)
	return database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]Operation, error) {
		return scanOperations(tx.Query(ctx, operationSelect+` WHERE (created_at,id)<($1,$2) ORDER BY created_at DESC,id DESC LIMIT $3`, before, beforeID, limit))
	})
}

const operationSelect = `SELECT id,tenant_id,operation_type,resource_type,resource_id,requested_generation,state,attempts,progress_category,result_code,safe_message,requested_by,started_at,completed_at,created_at,updated_at FROM operations.operations`

func normalizeBefore(before time.Time, id uuid.UUID) (time.Time, uuid.UUID) {
	if before.IsZero() {
		before = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	if id == uuid.Nil {
		id = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	}
	return before.UTC(), id
}

type scanner interface{ Scan(...any) error }

func scanOperation(row scanner) (Operation, error) {
	var item Operation
	err := row.Scan(&item.ID, &item.TenantID, &item.OperationType, &item.ResourceType, &item.ResourceID, &item.RequestedGeneration, &item.State, &item.Attempts, &item.ProgressCategory, &item.ResultCode, &item.SafeMessage, &item.RequestedBy, &item.StartedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, fmt.Errorf("%w: %w", ErrStorage, err)
	}
	return item, nil
}

func scanOperations(rows pgx.Rows, err error) ([]Operation, error) {
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStorage, err)
	}
	defer rows.Close()
	items := make([]Operation, 0)
	for rows.Next() {
		item, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStorage, err)
	}
	return items, nil
}
