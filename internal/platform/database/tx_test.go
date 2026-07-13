package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationsUpDownUp(t *testing.T) {
	postgres := testkit.StartPostgres(t)

	testkit.MigrationsUp(t, postgres.AdminDSN)
	assertFoundationExists(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	testkit.MigrationsDown(t, postgres.AdminDSN)
	assertFoundationAbsent(t, postgres.AdminDSN)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	assertFoundationExists(t, postgres.AdminDSN)
}

func TestApplicationRoleCannotBypassRLS(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)

	admin := openPool(t, postgres.AdminDSN)
	defer admin.Close()
	tenantID := uuid.New()
	statements := []string{
		"CREATE TABLE tenancy.rls_fixture (tenant_id uuid NOT NULL, secret text NOT NULL)",
		"ALTER TABLE tenancy.rls_fixture ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE tenancy.rls_fixture FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_fixture_isolation ON tenancy.rls_fixture USING (tenant_id = platform.current_tenant_id())",
		"GRANT USAGE ON SCHEMA tenancy, platform TO ajiasu_app",
		"GRANT EXECUTE ON FUNCTION platform.current_tenant_id() TO ajiasu_app",
		"GRANT SELECT ON tenancy.rls_fixture TO ajiasu_app",
	}
	for _, statement := range statements {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("create RLS fixture: %v", err)
		}
	}
	if _, err := admin.Exec(t.Context(), "INSERT INTO tenancy.rls_fixture (tenant_id, secret) VALUES ($1, 'isolated')", tenantID); err != nil {
		t.Fatalf("insert RLS fixture: %v", err)
	}

	tenant := openPool(t, postgres.TenantDSN)
	defer tenant.Close()
	var rowCount int
	if err := tenant.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.rls_fixture").Scan(&rowCount); err != nil {
		t.Fatalf("select RLS fixture without tenant context: %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("RLS fixture row count without tenant context = %d, want 0", rowCount)
	}

	var superuser, bypassRLS bool
	if err := tenant.QueryRow(t.Context(), "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&superuser, &bypassRLS); err != nil {
		t.Fatalf("inspect application login role: %v", err)
	}
	if superuser || bypassRLS {
		t.Fatalf("application login role has unsafe privileges: superuser=%t bypass_rls=%t", superuser, bypassRLS)
	}
	var groupCanLogin, groupBypassRLS bool
	if err := admin.QueryRow(t.Context(), "SELECT rolcanlogin, rolbypassrls FROM pg_roles WHERE rolname = 'ajiasu_app'").Scan(&groupCanLogin, &groupBypassRLS); err != nil {
		t.Fatalf("inspect ajiasu_app role: %v", err)
	}
	if groupCanLogin || groupBypassRLS {
		t.Fatalf("ajiasu_app role has unsafe privileges: login=%t bypass_rls=%t", groupCanLogin, groupBypassRLS)
	}
}

func TestTransactionLocalTenantContextDoesNotLeak(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)

	admin := openPool(t, postgres.AdminDSN)
	defer admin.Close()
	tenantID := uuid.New()
	actorID := uuid.New()
	statements := []string{
		"CREATE TABLE tenancy.tx_fixture (tenant_id uuid NOT NULL, secret text NOT NULL)",
		"ALTER TABLE tenancy.tx_fixture ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE tenancy.tx_fixture FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_tx_isolation ON tenancy.tx_fixture USING (tenant_id = platform.current_tenant_id())",
		"GRANT USAGE ON SCHEMA tenancy, platform TO ajiasu_app",
		"GRANT EXECUTE ON FUNCTION platform.current_tenant_id(), platform.current_actor_id() TO ajiasu_app",
		"GRANT SELECT ON tenancy.tx_fixture TO ajiasu_app",
	}
	for _, statement := range statements {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("create transaction fixture: %v", err)
		}
	}
	if _, err := admin.Exec(t.Context(), "INSERT INTO tenancy.tx_fixture (tenant_id, secret) VALUES ($1, 'isolated')", tenantID); err != nil {
		t.Fatalf("insert transaction fixture: %v", err)
	}

	tenant := openSingleConnectionPool(t, postgres.TenantDSN)
	defer tenant.Close()

	result, err := database.InTenantTx(t.Context(), tenant, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) (string, error) {
		var currentTenantID, currentActorID uuid.UUID
		var rowCount int
		if err := tx.QueryRow(ctx, "SELECT platform.current_tenant_id(), platform.current_actor_id(), (SELECT count(*) FROM tenancy.tx_fixture)").Scan(&currentTenantID, &currentActorID, &rowCount); err != nil {
			return "", err
		}
		if currentTenantID != tenantID || currentActorID != actorID || rowCount != 1 {
			t.Fatalf("transaction context = tenant %s actor %s rows %d", currentTenantID, currentActorID, rowCount)
		}
		return "committed", nil
	})
	if err != nil {
		t.Fatalf("commit tenant transaction: %v", err)
	}
	if result != "committed" {
		t.Fatalf("transaction result = %q, want committed", result)
	}
	assertTransactionSettingsEmpty(t, tenant)

	sentinel := errors.New("rollback requested")
	_, err = database.InTenantTx(t.Context(), tenant, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		var currentTenantID uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT platform.current_tenant_id()").Scan(&currentTenantID); err != nil {
			return struct{}{}, err
		}
		if currentTenantID != tenantID {
			t.Fatalf("rollback transaction tenant = %s, want %s", currentTenantID, tenantID)
		}
		return struct{}{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback transaction error = %v, want sentinel", err)
	}
	assertTransactionSettingsEmpty(t, tenant)
}

func TestInTenantTxRejectsZeroUUIDs(t *testing.T) {
	tests := []struct {
		name     string
		tenantID uuid.UUID
		actorID  uuid.UUID
	}{
		{name: "tenant", tenantID: uuid.Nil, actorID: uuid.New()},
		{name: "actor", tenantID: uuid.New(), actorID: uuid.Nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			_, err := database.InTenantTx(t.Context(), nil, tt.tenantID, tt.actorID, func(context.Context, pgx.Tx) (struct{}, error) {
				called = true
				return struct{}{}, nil
			})
			if err == nil {
				t.Fatal("InTenantTx() error = nil, want zero UUID rejection")
			}
			if called {
				t.Fatal("InTenantTx() invoked callback for zero UUID")
			}
		})
	}
}

func TestInTenantTxRollsBackBeforeRepanicking(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	tenant := openSingleConnectionPool(t, postgres.TenantDSN)
	var capturedTx pgx.Tx
	t.Cleanup(func() {
		if capturedTx != nil {
			_ = capturedTx.Rollback(context.Background())
		}
		tenant.Close()
	})

	panicValue := errors.New("panic sentinel")
	recovered := func() (value any) {
		defer func() {
			value = recover()
		}()
		_, _ = database.InTenantTx(t.Context(), tenant, uuid.New(), uuid.New(), func(_ context.Context, tx pgx.Tx) (struct{}, error) {
			capturedTx = tx
			panic(panicValue)
		})
		return nil
	}()
	if recovered != panicValue {
		t.Fatalf("recovered panic = %v, want original panic value", recovered)
	}
	if acquired := tenant.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("acquired connections after panic = %d, want 0 after rollback", acquired)
	}
	assertTransactionSettingsEmpty(t, tenant)
}

func TestInTenantTxCancelledContextRollsBackWithoutRetry(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	tenant := openSingleConnectionPool(t, postgres.TenantDSN)
	defer tenant.Close()

	ctx, cancel := context.WithCancel(t.Context())
	callbackCalls := 0
	_, err := database.InTenantTx(ctx, tenant, uuid.New(), uuid.New(), func(context.Context, pgx.Tx) (struct{}, error) {
		callbackCalls++
		cancel()
		return struct{}{}, context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InTenantTx() error = %v, want context cancellation", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("InTenantTx() callback calls = %d, want exactly 1", callbackCalls)
	}
	if acquired := tenant.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("acquired connections after cancellation = %d, want 0", acquired)
	}
	assertTransactionSettingsEmpty(t, tenant)
}

func TestInPlatformTxSetsOnlyActorAndDoesNotLeak(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	platform := openSingleConnectionPool(t, postgres.PlatformDSN)
	defer platform.Close()

	actorID := uuid.New()
	result, err := database.InPlatformTx(t.Context(), platform, actorID, func(ctx context.Context, tx pgx.Tx) (string, error) {
		var actorSetting string
		var tenantSetting *string
		if err := tx.QueryRow(ctx, "SELECT current_setting('app.actor_id', true), current_setting('app.tenant_id', true)").Scan(&actorSetting, &tenantSetting); err != nil {
			return "", err
		}
		if actorSetting != actorID.String() {
			t.Fatalf("platform actor setting = %q, want %q", actorSetting, actorID)
		}
		if tenantSetting != nil && *tenantSetting != "" {
			t.Fatalf("platform transaction set tenant context = %q", *tenantSetting)
		}
		return "platform", nil
	})
	if err != nil {
		t.Fatalf("commit platform transaction: %v", err)
	}
	if result != "platform" {
		t.Fatalf("platform transaction result = %q", result)
	}
	assertTransactionSettingsEmpty(t, platform)

	sentinel := errors.New("platform rollback requested")
	callbackCalls := 0
	_, err = database.InPlatformTx(t.Context(), platform, actorID, func(context.Context, pgx.Tx) (struct{}, error) {
		callbackCalls++
		return struct{}{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("platform rollback error = %v, want sentinel", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("platform callback calls = %d, want exactly 1", callbackCalls)
	}
	assertTransactionSettingsEmpty(t, platform)

	called := false
	_, err = database.InPlatformTx(t.Context(), nil, uuid.Nil, func(context.Context, pgx.Tx) (struct{}, error) {
		called = true
		return struct{}{}, nil
	})
	if err == nil || called {
		t.Fatalf("zero platform actor result: error=%v callback_called=%t", err, called)
	}
}

func assertFoundationExists(t *testing.T, dsn string) {
	t.Helper()
	pool := openPool(t, dsn)
	defer pool.Close()
	for _, schema := range []string{"platform", "identity", "tenancy", "audit"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists); err != nil {
			t.Fatalf("inspect schema %s: %v", schema, err)
		}
		if !exists {
			t.Fatalf("schema %s does not exist", schema)
		}
	}
	for _, function := range []string{"platform.current_tenant_id()", "platform.current_actor_id()"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), "SELECT to_regprocedure($1) IS NOT NULL", function).Scan(&exists); err != nil {
			t.Fatalf("inspect function %s: %v", function, err)
		}
		if !exists {
			t.Fatalf("function %s does not exist", function)
		}
	}
	for _, role := range []string{"ajiasu_app", "ajiasu_platform"} {
		var canLogin, superuser, createDB, createRole, replication, bypassRLS bool
		if err := pool.QueryRow(t.Context(), "SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls FROM pg_roles WHERE rolname = $1", role).Scan(&canLogin, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
			t.Fatalf("inspect group role %s: %v", role, err)
		}
		if canLogin || superuser || createDB || createRole || replication || bypassRLS {
			t.Fatalf("group role %s is unsafe: login=%t superuser=%t createdb=%t createrole=%t replication=%t bypass_rls=%t", role, canLogin, superuser, createDB, createRole, replication, bypassRLS)
		}
	}
	for _, schema := range []string{"platform", "identity", "tenancy", "audit"} {
		var publicUsage, appUsage, platformUsage bool
		if err := pool.QueryRow(t.Context(), `
SELECT EXISTS (
           SELECT 1
           FROM pg_namespace AS namespace,
                LATERAL aclexplode(COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))) AS acl
           WHERE namespace.nspname = $1
             AND acl.grantee = 0
             AND acl.privilege_type = 'USAGE'
       ),
       has_schema_privilege('ajiasu_app', $1, 'USAGE'),
       has_schema_privilege('ajiasu_platform', $1, 'USAGE')
`, schema).Scan(&publicUsage, &appUsage, &platformUsage); err != nil {
			t.Fatalf("inspect schema privileges for %s: %v", schema, err)
		}
		if publicUsage || !appUsage || !platformUsage {
			t.Fatalf("schema %s privileges: public=%t app=%t platform=%t", schema, publicUsage, appUsage, platformUsage)
		}
	}
	for _, function := range []string{"platform.current_tenant_id()", "platform.current_actor_id()"} {
		var publicExecute, appExecute, platformExecute bool
		if err := pool.QueryRow(t.Context(), `
SELECT EXISTS (
           SELECT 1
           FROM pg_proc AS procedure,
                LATERAL aclexplode(COALESCE(procedure.proacl, acldefault('f', procedure.proowner))) AS acl
           WHERE procedure.oid = to_regprocedure($1)
             AND acl.grantee = 0
             AND acl.privilege_type = 'EXECUTE'
       ),
       has_function_privilege('ajiasu_app', $1, 'EXECUTE'),
       has_function_privilege('ajiasu_platform', $1, 'EXECUTE')
`, function).Scan(&publicExecute, &appExecute, &platformExecute); err != nil {
			t.Fatalf("inspect function privileges for %s: %v", function, err)
		}
		if publicExecute || !appExecute || !platformExecute {
			t.Fatalf("function %s privileges: public=%t app=%t platform=%t", function, publicExecute, appExecute, platformExecute)
		}
	}
}

func assertFoundationAbsent(t *testing.T, dsn string) {
	t.Helper()
	pool := openPool(t, dsn)
	defer pool.Close()
	for _, schema := range []string{"platform", "identity", "tenancy", "audit"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists); err != nil {
			t.Fatalf("inspect schema %s: %v", schema, err)
		}
		if exists {
			t.Fatalf("schema %s still exists after down migration", schema)
		}
	}
	for _, role := range []string{"ajiasu_app", "ajiasu_platform"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
			t.Fatalf("inspect group role %s after down: %v", role, err)
		}
		if exists {
			t.Fatalf("group role %s still exists after down migration", role)
		}
	}
}

func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Fatalf("ping PostgreSQL pool: %v", err)
	}
	return pool
}

func openSingleConnectionPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL pool config: %v", err)
	}
	config.MaxConns = 1
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open single-connection PostgreSQL pool: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Fatalf("ping single-connection PostgreSQL pool: %v", err)
	}
	return pool
}

func assertTransactionSettingsEmpty(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	connection, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("reacquire pooled connection: %v", err)
	}
	defer connection.Release()
	var tenantSetting, actorSetting *string
	if err := connection.QueryRow(t.Context(), "SELECT current_setting('app.tenant_id', true), current_setting('app.actor_id', true)").Scan(&tenantSetting, &actorSetting); err != nil {
		t.Fatalf("read transaction-local settings after reuse: %v", err)
	}
	if settingIsNonEmpty(tenantSetting) || settingIsNonEmpty(actorSetting) {
		t.Fatalf("transaction-local settings leaked after reuse: tenant=%v actor=%v", tenantSetting, actorSetting)
	}
}

func settingIsNonEmpty(setting *string) bool {
	return setting != nil && *setting != ""
}
