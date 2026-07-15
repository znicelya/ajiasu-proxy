package identity

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestServiceTenantRLSAndPlatformExclusivity(t *testing.T) {
	db := startIdentityDatabase(t)
	tenantA, tenantB := uuid.New(), uuid.New()
	now := time.Now().UTC()
	if _, err := db.admin.Exec(t.Context(), "INSERT INTO tenancy.tenants(id,slug,name,state,version,created_at,updated_at) VALUES($1,'service-a','Tenant A','active',1,$3,$3),($2,'service-b','Tenant B','active',1,$3,$3)", tenantA, tenantB, now); err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceIdentityService(db.pools, nil)
	if err != nil {
		t.Fatal(err)
	}
	actorA := tenantServiceActor(tenantA)
	actorB := tenantServiceActor(tenantB)
	identityA, tokenA, err := service.Create(t.Context(), actorA, CreateServiceIdentityCommand{Scope: ServiceScopeTenant, TenantID: &tenantA, Name: "tenant-a-bot", Role: tenancy.Operator})
	if err != nil {
		t.Fatal(err)
	}
	identityB, _, err := service.Create(t.Context(), actorB, CreateServiceIdentityCommand{Scope: ServiceScopeTenant, TenantID: &tenantB, Name: "tenant-b-bot", Role: tenancy.Auditor})
	if err != nil {
		t.Fatal(err)
	}
	platformIdentity, _, err := service.Create(t.Context(), platformServiceActor(), CreateServiceIdentityCommand{Scope: ServiceScopePlatform, Name: "platform-bot", Role: tenancy.PlatformAdmin})
	if err != nil {
		t.Fatal(err)
	}
	countVisible := func(tenantID uuid.UUID) int {
		count, err := database.InTenantTx(t.Context(), db.pools.Tenant, tenantID, uuid.New(), func(ctx context.Context, tx pgx.Tx) (int, error) {
			var value int
			err := tx.QueryRow(ctx, "SELECT count(*) FROM identity.service_identities").Scan(&value)
			return value, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := countVisible(tenantA); got != 1 {
		t.Fatalf("tenant A visible=%d", got)
	}
	if got := countVisible(tenantB); got != 1 {
		t.Fatalf("tenant B visible=%d", got)
	}
	rows, err := database.InTenantTx(t.Context(), db.pools.Tenant, tenantB, uuid.New(), func(ctx context.Context, tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, "UPDATE identity.service_identities SET name='stolen' WHERE id=$1", identityA.ID)
		return tag.RowsAffected(), err
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("cross-tenant update rows=%d", rows)
	}
	var platformRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.service_identities WHERE id IN ($1,$2,$3)", identityA.ID, identityB.ID, platformIdentity.ID).Scan(&platformRows); err != nil {
		t.Fatal(err)
	}
	if platformRows != 3 {
		t.Fatalf("admin rows=%d", platformRows)
	}
	principal, err := service.Authenticate(t.Context(), serviceAuth(tokenA.Plaintext, ServiceScopeTenant, &tenantA, "203.0.113.20"))
	if err != nil || principal.IdentityID != identityA.ID {
		t.Fatalf("tenant auth principal=%#v err=%v", principal, err)
	}
	if _, err := service.Authenticate(t.Context(), serviceAuth(tokenA.Plaintext, ServiceScopeTenant, &tenantB, "203.0.113.20")); err == nil {
		t.Fatal("tenant A token authenticated in tenant B")
	}
}

func TestServiceMigrationDownToFiveAndUp(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	testkit.MigrationsDownTo(t, postgres.AdminDSN, 5)
	pool := openIdentityPool(t, postgres.AdminDSN)
	var identities, tokens, sessions bool
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('identity.service_identities') IS NOT NULL,to_regclass('identity.service_tokens') IS NOT NULL,to_regclass('identity.auth_sessions') IS NOT NULL").Scan(&identities, &tokens, &sessions); err != nil {
		t.Fatal(err)
	}
	if identities || tokens || !sessions {
		t.Fatalf("down state=%t/%t/%t", identities, tokens, sessions)
	}
	testkit.MigrationsUp(t, postgres.AdminDSN)
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('identity.service_identities') IS NOT NULL,to_regclass('identity.service_tokens') IS NOT NULL").Scan(&identities, &tokens); err != nil {
		t.Fatal(err)
	}
	if !identities || !tokens {
		t.Fatalf("up state=%t/%t", identities, tokens)
	}
}

func tenantServiceActor(tenantID uuid.UUID) ServiceActor {
	return ServiceActor{ActorID: uuid.New(), Scope: ServiceScopeTenant, TenantID: &tenantID, Role: tenancy.TenantAdmin, Metadata: AuthenticationMetadata{SourceIP: netip.MustParseAddr("203.0.113.11"), UserAgent: "tenant-service-test/1.0", RequestID: uuid.New()}}
}
