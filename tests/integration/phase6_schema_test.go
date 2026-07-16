package integration_test

import (
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
)

func TestPhase6SchemaMigratesAndForcesSchedulerRLS(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	admin := openPhase4Pool(t, postgres.AdminDSN)

	for _, table := range []string{"endpoint_assignments", "health_observations", "pool_cursors", "migration_attempts"} {
		var enabled, forced bool
		if err := admin.QueryRow(t.Context(), `SELECT relrowsecurity,relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='scheduler' AND c.relname=$1`, table).Scan(&enabled, &forced); err != nil { t.Fatal(err) }
		if !enabled || !forced { t.Fatalf("scheduler.%s RLS enabled=%t forced=%t", table, enabled, forced) }
	}

	testkit.MigrationsDownTo(t, postgres.AdminDSN, 10)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	var version int64
	if err := admin.QueryRow(t.Context(), `SELECT max(version_id) FILTER (WHERE is_applied) FROM public.goose_db_version`).Scan(&version); err != nil { t.Fatal(err) }
	if version != 11 { t.Fatalf("schema version=%d want 11", version) }
}
