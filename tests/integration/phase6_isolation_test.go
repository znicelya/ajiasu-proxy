package integration_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/accounts"
	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	"github.com/dnomd343/ajiasu-proxy/internal/secrets"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPhase6PostgresCapacityContentionAndHealthIsolation(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	admin := openPhase4Pool(t, postgres.AdminDSN)
	platform := openPhase4Pool(t, postgres.PlatformDSN)
	tenantPool := openPhase4Pool(t, postgres.TenantDSN)
	now := time.Now().UTC()
	tenantA, tenantB := uuid.New(), uuid.New()
	if _, err := admin.Exec(t.Context(), `INSERT INTO tenancy.tenants (id,slug,name,state,created_at,updated_at) VALUES ($1,'phase6-a','Phase 6 A','active',$3,$3),($2,'phase6-b','Phase 6 B','active',$3,$3)`, tenantA, tenantB, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO tenancy.tenant_quotas (tenant_id,created_at,updated_at) VALUES ($1,$3,$3),($2,$3,$3)`, tenantA, tenantB, now); err != nil {
		t.Fatal(err)
	}
	db := &database.Pools{Platform: platform, Tenant: tenantPool}
	ring, _ := keyring.NewAESGCM(bytes.Repeat([]byte{6}, 32))
	provider, _ := secrets.NewEnvelopeProvider(ring)
	service, err := accounts.NewService(db, provider, audit.NewService())
	if err != nil {
		t.Fatal(err)
	}
	actor := phase3Actor(t, tenantA)
	account, err := service.Create(t.Context(), actor, accounts.CreateCommand{Name: "phase6-capacity", MaxConcurrency: 1, Credential: accounts.Credential{Username: "fake", Password: "fake-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsByReplica := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, reserveErr := service.Reserve(t.Context(), actor, account.ID, uuid.New(), time.Minute)
			errorsByReplica <- reserveErr
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByReplica)
	succeeded, exhausted := 0, 0
	for reserveErr := range errorsByReplica {
		switch {
		case reserveErr == nil:
			succeeded++
		case errors.Is(reserveErr, accounts.ErrCapacityExhausted):
			exhausted++
		default:
			t.Fatalf("reserve error=%v", reserveErr)
		}
	}
	if succeeded != 1 || exhausted != 1 {
		t.Fatalf("capacity results succeeded=%d exhausted=%d", succeeded, exhausted)
	}

	for _, tenantID := range []uuid.UUID{tenantA, tenantB} {
		if _, err := platform.Exec(t.Context(), `INSERT INTO scheduler.health_observations (tenant_id,resource_type,resource_id,dimension,state,generation,last_sequence,last_transition_at,updated_at) VALUES ($1,'account',$2,'account','healthy',1,1,$3,$3)`, tenantID, uuid.New(), now); err != nil {
			t.Fatal(err)
		}
	}
	visible, err := database.InTenantTx(t.Context(), tenantPool, tenantA, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler.health_observations`).Scan(&count)
		return count, err
	})
	if err != nil || visible != 1 {
		t.Fatalf("tenant A visible health=%d err=%v", visible, err)
	}
	crossTenant, err := database.InTenantTx(t.Context(), tenantPool, tenantA, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler.health_observations WHERE tenant_id=$1`, tenantB).Scan(&count)
		return count, err
	})
	if err != nil || crossTenant != 0 {
		t.Fatalf("cross-tenant health=%d err=%v", crossTenant, err)
	}
}
