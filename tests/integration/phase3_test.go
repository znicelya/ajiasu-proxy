package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/accounts"
	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/platform/keyring"
	accountpools "github.com/znicelya/ajiasu-proxy/internal/pools"
	"github.com/znicelya/ajiasu-proxy/internal/secrets"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/znicelya/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase3AccountsSecretsPoolsQuotasCapacityAndRLS(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	admin := openPool(t, postgres.AdminDSN)
	tenantPool := openPool(t, postgres.TenantDSN)
	db := &database.Pools{Tenant: tenantPool, Platform: tenantPool}

	tenantID := uuid.New()
	now := time.Now().UTC()
	if _, err := admin.Exec(t.Context(), `INSERT INTO tenancy.tenants (id,slug,name,state,created_at,updated_at) VALUES ($1,'phase3','Phase 3','active',$2,$2)`, tenantID, now); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO tenancy.tenant_quotas (tenant_id,max_accounts,created_at,updated_at) VALUES ($1,2,$2,$2)`, tenantID, now); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	actor := phase3Actor(t, tenantID)
	ring, err := keyring.NewAESGCM(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := secrets.NewEnvelopeProvider(ring)
	if err != nil {
		t.Fatal(err)
	}
	accountService, err := accounts.NewService(db, provider, audit.NewService())
	if err != nil {
		t.Fatal(err)
	}
	poolService, err := accountpools.NewService(db, audit.NewService())
	if err != nil {
		t.Fatal(err)
	}

	first, err := accountService.Create(t.Context(), actor, accounts.CreateCommand{Name: "primary", Labels: map[string]string{"region": "cn"}, MaxConcurrency: 2, Credential: accounts.Credential{Username: "credential-user-canary", Password: "credential-password-canary"}})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	encoded, _ := json.Marshal(first)
	if bytes.Contains(encoded, []byte("credential-user-canary")) || bytes.Contains(encoded, []byte("credential-password-canary")) {
		t.Fatal("account response exposed plaintext credential")
	}
	var ciphertext, wrapped []byte
	var keyID string
	if err := admin.QueryRow(t.Context(), `SELECT ciphertext,wrapped_dek,key_id FROM accounts.account_credentials WHERE tenant_id=$1 AND account_id=$2 AND retired_at IS NULL`, tenantID, first.ID).Scan(&ciphertext, &wrapped, &keyID); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("credential-password-canary")) {
		t.Fatal("stored ciphertext contains plaintext")
	}
	wrongRing, _ := keyring.NewAESGCM(bytes.Repeat([]byte{8}, 32))
	wrongProvider, _ := secrets.NewEnvelopeProvider(wrongRing)
	if _, err = wrongProvider.Open(t.Context(), secrets.Context{TenantID: tenantID, AccountID: first.ID, Version: 1, Purpose: secrets.AccountCredentialPurpose}, secrets.SealedSecret{Provider: secrets.EnvelopeProviderName, KeyID: keyID, Ciphertext: ciphertext, WrappedDEK: wrapped}); err == nil {
		t.Fatal("wrong master key decrypted credential")
	}
	rotated, err := accountService.RotateCredential(t.Context(), actor, first.ID, accounts.Credential{Username: "rotated-user", Password: "rotated-password"})
	if err != nil || rotated.Version != 2 {
		t.Fatalf("rotate credential = %#v, %v", rotated, err)
	}
	var versions, active int
	if err = admin.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE retired_at IS NULL) FROM accounts.account_credentials WHERE tenant_id=$1 AND account_id=$2`, tenantID, first.ID).Scan(&versions, &active); err != nil || versions != 2 || active != 1 {
		t.Fatalf("credential history versions=%d active=%d err=%v", versions, active, err)
	}
	var leaked bool
	if err = admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit.audit_events WHERE details::text LIKE '%credential-password-canary%' OR details::text LIKE '%rotated-password%') OR EXISTS(SELECT 1 FROM platform.outbox_events WHERE payload::text LIKE '%credential-password-canary%' OR payload::text LIKE '%rotated-password%')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("credential canary leaked into audit or outbox")
	}

	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for _, name := range []string{"concurrent-a", "concurrent-b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := accountService.Create(context.Background(), actor, accounts.CreateCommand{Name: name, Credential: accounts.Credential{Username: "u", Password: "p"}})
			outcomes <- err
		}(name)
	}
	wg.Wait()
	close(outcomes)
	successes, quotaFailures := 0, 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if errors.Is(err, accounts.ErrQuotaExceeded) {
			quotaFailures++
		} else {
			t.Fatalf("concurrent create error: %v", err)
		}
	}
	if successes != 1 || quotaFailures != 1 {
		t.Fatalf("quota outcomes successes=%d failures=%d", successes, quotaFailures)
	}
	maxAccounts := 4
	quota, err := tenancy.NewService(db, audit.NewService()).UpdateQuota(t.Context(), actor, tenancy.UpdateQuota{ExpectedVersion: 1, MaxAccounts: &maxAccounts})
	if err != nil || quota.Version != 2 {
		t.Fatalf("update quota = %#v, %v", quota, err)
	}
	results, err := accountService.BulkImport(t.Context(), actor, []accounts.CreateCommand{
		{Name: "primary", Credential: accounts.Credential{Username: "duplicate", Password: "duplicate-secret"}},
		{Name: "bulk-created", Credential: accounts.Credential{Username: "bulk", Password: "bulk-secret"}},
	})
	if err != nil || len(results) != 2 || results[0].Code != "already_exists" || results[1].Code != "created" || results[1].AccountID == nil {
		t.Fatalf("bulk import results = %#v, %v", results, err)
	}

	pool, err := poolService.Create(t.Context(), actor, accountpools.CreateCommand{Name: "cn-pool", Strategy: accountpools.LeastConnections, Selector: map[string]string{"region": "cn"}})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if _, err = poolService.AddMembership(t.Context(), actor, pool.ID, accountpools.AddMembershipCommand{AccountID: first.ID}); err != nil {
		t.Fatalf("add membership: %v", err)
	}
	reservation, err := accountService.Reserve(t.Context(), actor, first.ID, uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	capacity, err := poolService.Capacity(t.Context(), actor, pool.ID)
	if err != nil {
		t.Fatalf("capacity: %v", err)
	}
	if capacity.TotalConcurrency != 2 || capacity.ReservedConcurrency != 1 || capacity.AvailableConcurrency != 1 {
		t.Fatalf("capacity = %#v", capacity)
	}
	if err = accountService.Release(t.Context(), actor, reservation.ID); err != nil {
		t.Fatalf("release: %v", err)
	}

	otherTenant := uuid.New()
	visible, err := database.InTenantTx(t.Context(), tenantPool, otherTenant, uuid.New(), func(ctx context.Context, tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts.accounts`).Scan(&count)
		return count, err
	})
	if err != nil {
		t.Fatalf("cross-tenant query: %v", err)
	}
	if visible != 0 {
		t.Fatalf("cross-tenant RLS exposed %d accounts", visible)
	}
}

func phase3Actor(t *testing.T, tenantID uuid.UUID) tenancy.TenantActor {
	t.Helper()
	actor, err := tenancy.NewTenantActor(tenancy.Subject{ActorID: uuid.New(), TenantGrants: []tenancy.TenantGrant{{TenantID: tenantID, Role: tenancy.TenantAdmin}}}, tenantID, tenancy.ActorMetadata{ActorType: "user", SourceIP: netip.MustParseAddr("127.0.0.1"), UserAgent: "phase3-test", RequestID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	return actor
}
func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool
}
