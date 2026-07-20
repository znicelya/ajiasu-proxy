package gateways

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDatabaseSnapshotProviderPublishesOnlyCommittedCurrentAssignments(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	admin, err := pgxpool.New(t.Context(), postgres.AdminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	platform, err := pgxpool.New(t.Context(), postgres.PlatformDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer platform.Close()

	now := time.Unix(1_800_000_000, 0).UTC()
	tenantID, accountID, nodeID := uuid.New(), uuid.New(), uuid.New()
	endpointID, assignmentID, runnerID := uuid.New(), uuid.New(), uuid.New()
	gatewayID := uuid.New()
	activeCredentialID, revokedCredentialID := uuid.New(), uuid.New()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tenancy.tenants (id,slug,name,state,created_at,updated_at) VALUES ($1,'snapshot-provider','Snapshot Provider','active',$2,$2)`, []any{tenantID, now}},
		{`INSERT INTO accounts.accounts (id,tenant_id,name,normalized_name,created_at,updated_at) VALUES ($1,$2,'Account','account',$3,$3)`, []any{accountID, tenantID, now}},
		{`INSERT INTO nodes.nodes (id,name,normalized_name,created_at,updated_at) VALUES ($1,'Node','node',$2,$2)`, []any{nodeID, now}},
		{`INSERT INTO endpoints.proxy_endpoints (id,tenant_id,name,normalized_name,binding_mode,account_id,node_id,desired_runner_state,lifecycle_state,desired_generation,created_at,updated_at) VALUES ($1,$2,'Endpoint','endpoint','fixed',$3,$4,'running','active',3,$5,$5)`, []any{endpointID, tenantID, accountID, nodeID, now}},
		{`INSERT INTO endpoints.access_profiles (tenant_id,endpoint_id,protocols,policy_hash,created_at,updated_at) VALUES ($1,$2,ARRAY['http','connect']::text[],'policy-hash',$3,$3)`, []any{tenantID, endpointID, now}},
		{`INSERT INTO endpoints.proxy_credentials (id,tenant_id,endpoint_id,public_identifier,verifier,created_at,updated_at) VALUES ($1,$2,$3,'active-credential','active-verifier-value-with-minimum-length',$4,$4)`, []any{activeCredentialID, tenantID, endpointID, now}},
		{`INSERT INTO endpoints.proxy_credentials (id,tenant_id,endpoint_id,public_identifier,verifier,revoked_at,created_at,updated_at) VALUES ($1,$2,$3,'revoked-credential','revoked-verifier-value-with-minimum-length',$4,$4,$4)`, []any{revokedCredentialID, tenantID, endpointID, now}},
		{`INSERT INTO scheduler.endpoint_assignments (tenant_id,endpoint_id,assignment_id,account_id,node_id,runner_id,desired_generation,state,valid_until,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,3,'assigned',$7,$8,$8)`, []any{tenantID, endpointID, assignmentID, accountID, nodeID, runnerID, now.Add(10 * time.Minute), now}},
		{`INSERT INTO gateways.gateways (id,name,normalized_name,certificate_fingerprint,state,connectivity_state,created_at,updated_at) VALUES ($1,'Snapshot Gateway','snapshot-gateway','0123456789abcdef0123456789abcdef','active','online',$2,$2)`, []any{gatewayID, now}},
	}
	for _, statement := range statements {
		if _, err := admin.Exec(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = 7
	}
	provider, err := NewDatabaseSnapshotProvider(&database.Pools{Platform: platform}, seed)
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return now }
	snapshot, err := provider.Snapshot(t.Context(), gatewayID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version == 0 || len(snapshot.Routes) != 1 {
		t.Fatalf("snapshot version=%d routes=%d", snapshot.Version, len(snapshot.Routes))
	}
	route := snapshot.Routes[0]
	if route.AssignmentID != assignmentID || route.AssignmentGeneration != 3 || route.Grant.RunnerID != runnerID {
		t.Fatalf("route assignment metadata=%#v", route)
	}
	if len(route.Credentials) != 1 || route.Credentials[0].ID != activeCredentialID {
		t.Fatalf("route credentials=%#v", route.Credentials)
	}
	publicKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if err := route.Grant.Verify(publicKey, gatewayID, now); err != nil {
		t.Fatal(err)
	}
	recovery, err := provider.Snapshot(t.Context(), gatewayID)
	if err != nil || recovery.Version <= snapshot.Version {
		t.Fatalf("recovery version=%d initial=%d error=%v", recovery.Version, snapshot.Version, err)
	}
}
