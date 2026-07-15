package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type transactionContext struct {
	tx       pgx.Tx
	tenantID *uuid.UUID
	actorID  uuid.UUID
}

type transactionContextKey struct{}

func ContextWithPlatformTx(ctx context.Context, tx pgx.Tx, actorID uuid.UUID) context.Context {
	return context.WithValue(ctx, transactionContextKey{}, transactionContext{tx: tx, actorID: actorID})
}

func ContextWithTenantTx(ctx context.Context, tx pgx.Tx, tenantID, actorID uuid.UUID) context.Context {
	copy := tenantID
	return context.WithValue(ctx, transactionContextKey{}, transactionContext{tx: tx, tenantID: &copy, actorID: actorID})
}

func InTenantTx[T any](ctx context.Context, pool *pgxpool.Pool, tenantID, actorID uuid.UUID, fn func(context.Context, pgx.Tx) (T, error)) (result T, err error) {
	if tenantID == uuid.Nil {
		return result, errors.New("tenant ID must not be zero")
	}
	if actorID == uuid.Nil {
		return result, errors.New("actor ID must not be zero")
	}
	if existing, ok := ctx.Value(transactionContextKey{}).(transactionContext); ok {
		if existing.tx == nil || existing.tenantID == nil || *existing.tenantID != tenantID || existing.actorID != actorID {
			return result, errors.New("tenant transaction context does not match requested scope")
		}
		return fn(ctx, existing.tx)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin tenant transaction: %w", err)
	}
	defer rollback(ctx, tx)

	if _, err = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String()); err != nil {
		return result, fmt.Errorf("set tenant transaction context: %w", err)
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('app.actor_id', $1, true)", actorID.String()); err != nil {
		return result, fmt.Errorf("set actor transaction context: %w", err)
	}

	result, err = fn(ctx, tx)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		var zero T
		return zero, fmt.Errorf("commit tenant transaction: %w", err)
	}
	return result, nil
}

func InPlatformTx[T any](ctx context.Context, pool *pgxpool.Pool, actorID uuid.UUID, fn func(context.Context, pgx.Tx) (T, error)) (result T, err error) {
	if actorID == uuid.Nil {
		return result, errors.New("actor ID must not be zero")
	}
	if existing, ok := ctx.Value(transactionContextKey{}).(transactionContext); ok {
		if existing.tx == nil || existing.tenantID != nil || existing.actorID != actorID {
			return result, errors.New("platform transaction context does not match requested scope")
		}
		return fn(ctx, existing.tx)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin platform transaction: %w", err)
	}
	defer rollback(ctx, tx)

	if _, err = tx.Exec(ctx, "SELECT set_config('app.actor_id', $1, true)", actorID.String()); err != nil {
		return result, fmt.Errorf("set actor transaction context: %w", err)
	}
	result, err = fn(ctx, tx)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		var zero T
		return zero, fmt.Errorf("commit platform transaction: %w", err)
	}
	return result, nil
}

func rollback(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}
