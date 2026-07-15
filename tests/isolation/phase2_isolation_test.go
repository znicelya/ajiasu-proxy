package isolation_test

import (
	"context"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase2TenantRLSCannotReadOrMutateAnotherTenant(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	admin, err := pgxpool.New(t.Context(), postgres.AdminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	pools, err := database.OpenPools(t.Context(), config.Database{
		Normal: config.DatabasePool{DSN: postgres.TenantDSN, MaxOpenConnections: 2}, Platform: config.DatabasePool{DSN: postgres.PlatformDSN, MaxOpenConnections: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	tenantA, tenantB := uuid.New(), uuid.New()
	identityA, identityB := uuid.New(), uuid.New()
	serviceA, serviceB := uuid.New(), uuid.New()
	now := time.Now().UTC()
	if _, err := admin.Exec(t.Context(), `
INSERT INTO identity.user_identities(id,tenant_eligible,disabled_at,version,created_at,updated_at)
VALUES($1,true,NULL,1,$3,$3),($2,true,NULL,1,$3,$3)
`, identityA, identityB, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `
INSERT INTO tenancy.tenants(id,slug,name,state,version,created_at,updated_at)
VALUES($1,'isolation-a','Isolation A','active',1,$3,$3),($2,'isolation-b','Isolation B','active',1,$3,$3)
`, tenantA, tenantB, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `
INSERT INTO identity.service_identities(id,scope,tenant_id,name,disabled_at,version,created_at,updated_at)
VALUES($1,'tenant',$3,'bot-a',NULL,1,$5,$5),($2,'tenant',$4,'bot-b',NULL,1,$5,$5)
`, serviceA, serviceB, tenantA, tenantB, now); err != nil {
		t.Fatal(err)
	}

	countA, err := database.InTenantTx(t.Context(), pools.Tenant, tenantA, identityA, func(ctx context.Context, tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(ctx, "SELECT count(*) FROM identity.service_identities").Scan(&count)
		return count, err
	})
	if err != nil || countA != 1 {
		t.Fatalf("tenant A visible=%d err=%v", countA, err)
	}
	rows, err := database.InTenantTx(t.Context(), pools.Tenant, tenantB, identityB, func(ctx context.Context, tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, "UPDATE identity.service_identities SET name='stolen' WHERE id=$1", serviceA)
		return tag.RowsAffected(), err
	})
	if err != nil || rows != 0 {
		t.Fatalf("cross-tenant update rows=%d err=%v", rows, err)
	}
	var name string
	if err := admin.QueryRow(t.Context(), "SELECT name FROM identity.service_identities WHERE id=$1", serviceA).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "bot-a" {
		t.Fatalf("cross-tenant mutation changed name=%q", name)
	}
}
