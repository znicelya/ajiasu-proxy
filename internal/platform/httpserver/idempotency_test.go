package httpserver

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestIdempotencyReplayConflictAndAtomicRollback(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	pools, err := database.OpenPools(t.Context(), config.Database{
		Normal:   config.DatabasePool{DSN: postgres.TenantDSN, MaxOpenConnections: 4, MinIdleConnections: 0},
		Platform: config.DatabasePool{DSN: postgres.PlatformDSN, MaxOpenConnections: 4, MinIdleConnections: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	store, err := NewIdempotencyStore(pools)
	if err != nil {
		t.Fatal(err)
	}
	request := IdempotencyRequest{Scope: IdempotencyScopePlatform, ActorID: uuid.New(), Method: "POST", CanonicalRoute: "/api/v1/tenants", Key: "create-tenant-1", Body: []byte(`{"name":"A"}`)}
	calls := 0
	operation := func(context.Context, pgx.Tx) (StoredResponse, error) {
		calls++
		return StoredResponse{Status: 201, Body: []byte(`{"id":"created"}`)}, nil
	}
	first, replayed, err := store.Execute(t.Context(), request, operation)
	if err != nil || replayed || first.Status != 201 {
		t.Fatalf("first = %#v replayed=%t err=%v", first, replayed, err)
	}
	second, replayed, err := store.Execute(t.Context(), request, operation)
	if err != nil || !replayed || !bytes.Equal(first.Body, second.Body) || calls != 1 {
		t.Fatalf("second = %#v replayed=%t calls=%d err=%v", second, replayed, calls, err)
	}
	conflict := request
	conflict.Body = []byte(`{"name":"B"}`)
	if _, _, err := store.Execute(t.Context(), conflict, operation); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}

	rolledBack := request
	rolledBack.Key = "rollback"
	failed := func(ctx context.Context, tx pgx.Tx) (StoredResponse, error) {
		if _, err := tx.Exec(ctx, "CREATE TEMP TABLE idempotency_atomic_probe(value integer)"); err != nil {
			return StoredResponse{}, err
		}
		return StoredResponse{}, errors.New("business failure")
	}
	if _, _, err := store.Execute(t.Context(), rolledBack, failed); err == nil {
		t.Fatal("failing operation unexpectedly succeeded")
	}
	if _, _, err := store.Execute(t.Context(), rolledBack, operation); err != nil {
		t.Fatalf("retry after rollback = %v", err)
	}
}
