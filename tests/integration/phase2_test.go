package integration_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/identity"
	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/znicelya/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase2ExitGateCoreLifecycle(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	admin, err := pgxpool.New(t.Context(), postgres.AdminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	pools := openPools(t, postgres)
	auditService := audit.NewService()
	tenancyService := tenancy.NewService(pools, auditService)
	serviceIdentities, err := identity.NewServiceIdentityService(pools, auditService)
	if err != nil {
		t.Fatal(err)
	}

	platformIdentity := createIdentity(t, admin)
	tenantAAdmin := createIdentity(t, admin)
	tenantBAdmin := createIdentity(t, admin)
	jitIdentity := createIdentity(t, admin)
	var jitMemberships int
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.memberships WHERE identity_id=$1", jitIdentity).Scan(&jitMemberships); err != nil {
		t.Fatal(err)
	}
	if jitMemberships != 0 {
		t.Fatalf("JIT-style identity memberships=%d", jitMemberships)
	}

	platformActor, err := tenancy.NewPlatformActor(
		tenancy.Subject{ActorID: platformIdentity, PlatformRoles: []tenancy.Role{tenancy.PlatformAdmin}},
		actorMetadata("platform-admin"),
	)
	if err != nil {
		t.Fatal(err)
	}
	tenantA, err := tenancyService.CreateTenant(t.Context(), platformActor, tenancy.CreateTenant{Slug: "phase2-a", Name: "Phase 2 A", InitialAdminIdentityID: tenantAAdmin})
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := tenancyService.CreateTenant(t.Context(), platformActor, tenancy.CreateTenant{Slug: "phase2-b", Name: "Phase 2 B", InitialAdminIdentityID: tenantBAdmin})
	if err != nil {
		t.Fatal(err)
	}
	name := "Phase 2 A Updated"
	updated, err := tenancyService.UpdateTenant(t.Context(), platformActor, tenancy.UpdateTenant{TenantID: tenantA.ID, ExpectedVersion: tenantA.Version, Name: &name})
	if err != nil || updated.Version != tenantA.Version+1 {
		t.Fatalf("update=%#v err=%v", updated, err)
	}
	if _, err := tenancyService.UpdateTenant(t.Context(), platformActor, tenancy.UpdateTenant{TenantID: tenantA.ID, ExpectedVersion: tenantA.Version, Name: &name}); !errors.Is(err, tenancy.ErrVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}

	tenantASubject := tenancy.Subject{ActorID: tenantAAdmin, TenantGrants: []tenancy.TenantGrant{{TenantID: tenantA.ID, Role: tenancy.TenantAdmin}}}
	if _, err := tenancy.NewTenantActor(tenantASubject, tenantB.ID, actorMetadata("tenant-a-admin")); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("tenant A escalated into tenant B: %v", err)
	}
	serviceActor := identity.ServiceActor{
		ActorID: tenantAAdmin, Scope: identity.ServiceScopeTenant, TenantID: &tenantA.ID, Role: tenancy.TenantAdmin,
		Metadata: identity.AuthenticationMetadata{SourceIP: netip.MustParseAddr("203.0.113.20"), UserAgent: "phase2-integration/1.0", RequestID: uuid.New()},
	}
	serviceIdentity, token, err := serviceIdentities.Create(t.Context(), serviceActor, identity.CreateServiceIdentityCommand{
		Scope: identity.ServiceScopeTenant, TenantID: &tenantA.ID, Name: "phase2-bot", Role: tenancy.Operator,
	})
	if err != nil || token.Plaintext == "" {
		t.Fatalf("service identity=%#v token=%#v err=%v", serviceIdentity, token.ServiceToken, err)
	}
	listed, err := serviceIdentities.List(t.Context(), serviceActor, time.Unix(0, 0).UTC(), uuid.Nil, 50)
	if err != nil || len(listed) != 1 || listed[0].ID != serviceIdentity.ID {
		t.Fatalf("listed identities=%#v err=%v", listed, err)
	}

	reader, err := audit.NewReader(pools)
	if err != nil {
		t.Fatal(err)
	}
	events, err := reader.List(t.Context(), audit.ReadRequest{ActorID: tenantAAdmin, TenantID: &tenantA.ID, After: time.Unix(0, 0).UTC(), PageSize: 200})
	if err != nil || len(events) < 5 {
		t.Fatalf("tenant audit events=%d err=%v", len(events), err)
	}

	store, err := httpserver.NewIdempotencyStore(pools)
	if err != nil {
		t.Fatal(err)
	}
	idempotencyRequest := httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopePlatform, ActorID: platformIdentity, Method: "POST", CanonicalRoute: "/api/v1/tenants", Key: "phase2-persistence", Body: []byte(`{"probe":true}`)}
	first, replayed, err := store.ExecuteJSON(t.Context(), idempotencyRequest, func(context.Context) (int, any, error) {
		return 201, map[string]string{"result": "persisted"}, nil
	})
	if err != nil || replayed {
		t.Fatalf("first idempotency response=%#v replayed=%t err=%v", first, replayed, err)
	}
	sessions, err := identity.NewSessionService(pools, auditService, identity.SessionCookieConfig{Name: "phase2_session", Path: "/api/v1", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	_, sessionToken, _, err := sessions.CreateSession(t.Context(), platformIdentity)
	if err != nil {
		t.Fatal(err)
	}
	pools.Close()
	postgres.Restart(t)
	pools = openPools(t, postgres)
	sessions, _ = identity.NewSessionService(pools, audit.NewService(), identity.SessionCookieConfig{Name: "phase2_session", Path: "/api/v1", Development: true})
	if authenticated, err := sessions.AuthenticateSession(t.Context(), sessionToken.Plaintext); err != nil || authenticated.Session.IdentityID != platformIdentity {
		t.Fatalf("session after PostgreSQL restart=%#v err=%v", authenticated.Session, err)
	}
	store, _ = httpserver.NewIdempotencyStore(pools)
	second, replayed, err := store.ExecuteJSON(t.Context(), idempotencyRequest, func(context.Context) (int, any, error) {
		t.Fatal("persisted idempotency operation executed twice")
		return 500, nil, nil
	})
	if err != nil || !replayed || string(second.Body) != string(first.Body) {
		t.Fatalf("persisted replay=%#v replayed=%t err=%v", second, replayed, err)
	}
}

func openPools(t *testing.T, postgres *testkit.Postgres) *database.Pools {
	t.Helper()
	pools, err := database.OpenPools(t.Context(), config.Database{
		Normal:   config.DatabasePool{DSN: postgres.TenantDSN, MaxOpenConnections: 4},
		Platform: config.DatabasePool{DSN: postgres.PlatformDSN, MaxOpenConnections: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	return pools
}

func createIdentity(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	if _, err := pool.Exec(t.Context(), "INSERT INTO identity.user_identities(id,tenant_eligible,disabled_at,version,created_at,updated_at) VALUES($1,true,NULL,1,$2,$2)", id, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func actorMetadata(actorType string) tenancy.ActorMetadata {
	return tenancy.ActorMetadata{ActorType: actorType, SourceIP: netip.MustParseAddr("203.0.113.10"), UserAgent: "phase2-integration/1.0", RequestID: uuid.New()}
}
