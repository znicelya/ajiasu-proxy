package integration_test

import (
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/testkit"
)

func TestPhase5SchemaMigratesAndForcesTenantRLS(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	admin := openPhase4Pool(t, postgres.AdminDSN)

	for _, table := range []string{"access_profiles", "proxy_credentials"} {
		var enabled, forced bool
		if err := admin.QueryRow(t.Context(), `SELECT relrowsecurity,relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='endpoints' AND c.relname=$1`, table).Scan(&enabled, &forced); err != nil {
			t.Fatal(err)
		}
		if !enabled || !forced {
			t.Fatalf("endpoints.%s RLS enabled=%t forced=%t", table, enabled, forced)
		}
	}

	testkit.MigrationsDownTo(t, postgres.AdminDSN, 9)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	var version int64
	if err := admin.QueryRow(t.Context(), `SELECT max(version_id) FILTER (WHERE is_applied) FROM public.goose_db_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 11 {
		t.Fatalf("schema version=%d want 11", version)
	}
}
