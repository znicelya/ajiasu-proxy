package database_test

import (
	"context"
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/testkit"
)

func TestOpenPoolsUsesSeparateCredentialsAndValidatesRoles(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)

	cfg := config.Database{
		Normal: config.DatabasePool{
			DSN:                postgres.TenantDSN,
			MaxOpenConnections: 3,
			MinIdleConnections: 1,
		},
		Platform: config.DatabasePool{
			DSN:                postgres.PlatformDSN,
			MaxOpenConnections: 2,
			MinIdleConnections: 1,
		},
	}
	pools, err := database.OpenPools(t.Context(), cfg)
	if err != nil {
		t.Fatalf("OpenPools() error = %v", err)
	}

	var tenantUser, platformUser string
	if err := pools.Tenant.QueryRow(t.Context(), "SELECT current_user").Scan(&tenantUser); err != nil {
		t.Fatalf("query tenant pool user: %v", err)
	}
	if err := pools.Platform.QueryRow(t.Context(), "SELECT current_user").Scan(&platformUser); err != nil {
		t.Fatalf("query platform pool user: %v", err)
	}
	if tenantUser != "ajiasu_test_tenant" || platformUser != "ajiasu_test_platform" || tenantUser == platformUser {
		t.Fatalf("pool users = tenant %q platform %q", tenantUser, platformUser)
	}
	if got := pools.Tenant.Config().MaxConns; got != 3 {
		t.Fatalf("tenant MaxConns = %d, want 3", got)
	}
	if got := pools.Tenant.Config().MinIdleConns; got != 1 {
		t.Fatalf("tenant MinIdleConns = %d, want configured idle target 1", got)
	}
	if got := pools.Platform.Config().MaxConns; got != 2 {
		t.Fatalf("platform MaxConns = %d, want 2", got)
	}
	pools.Close()

	legacyCfg := cfg
	legacyCfg.Normal.MinIdleConnections = 0
	legacyCfg.Normal.MaxIdleConnections = 1
	legacyCfg.Platform.MinIdleConnections = 0
	legacyCfg.Platform.MaxIdleConnections = 1
	legacyPools, err := database.OpenPools(t.Context(), legacyCfg)
	if err != nil {
		t.Fatalf("OpenPools() legacy idle aliases error = %v", err)
	}
	if legacyPools.Tenant.Config().MinIdleConns != 1 || legacyPools.Platform.Config().MinIdleConns != 1 {
		t.Fatalf("legacy idle aliases were not mapped to pgx MinIdleConns")
	}
	legacyPools.Close()

	conflictingCfg := cfg
	conflictingCfg.Normal.MaxIdleConnections = 2
	invalidPools, err := database.OpenPools(t.Context(), conflictingCfg)
	if err == nil {
		invalidPools.Close()
		t.Fatal("OpenPools() accepted conflicting idle connection aliases")
	}

	swapped := cfg
	swapped.Normal.DSN = postgres.PlatformDSN
	swapped.Platform.DSN = postgres.TenantDSN
	invalidPools, err = database.OpenPools(t.Context(), swapped)
	if err == nil {
		invalidPools.Close()
		t.Fatal("OpenPools() accepted swapped tenant and platform roles")
	}

	admin := openPool(t, postgres.AdminDSN)
	defer admin.Close()

	unsafeRoleStatements := []struct {
		name  string
		apply string
		reset string
	}{
		{name: "noinherit", apply: "ALTER ROLE ajiasu_test_tenant NOINHERIT", reset: "ALTER ROLE ajiasu_test_tenant INHERIT"},
		{name: "superuser", apply: "ALTER ROLE ajiasu_test_tenant SUPERUSER", reset: "ALTER ROLE ajiasu_test_tenant NOSUPERUSER"},
		{name: "bypassrls", apply: "ALTER ROLE ajiasu_test_tenant BYPASSRLS", reset: "ALTER ROLE ajiasu_test_tenant NOBYPASSRLS"},
	}
	for _, tt := range unsafeRoleStatements {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := admin.Exec(t.Context(), tt.apply); err != nil {
				t.Fatalf("apply unsafe role attribute: %v", err)
			}
			t.Cleanup(func() {
				if _, err := admin.Exec(context.Background(), tt.reset); err != nil {
					t.Errorf("reset unsafe role attribute: %v", err)
				}
			})
			invalidPools, err := database.OpenPools(t.Context(), cfg)
			if err == nil {
				invalidPools.Close()
				t.Fatal("OpenPools() accepted an unsafe tenant login role")
			}
			if _, err := admin.Exec(t.Context(), tt.reset); err != nil {
				t.Fatalf("reset unsafe role attribute: %v", err)
			}
		})
	}

	if _, err := admin.Exec(t.Context(), "GRANT ajiasu_platform TO ajiasu_test_tenant"); err != nil {
		t.Fatalf("grant platform role to tenant login: %v", err)
	}
	invalidPools, err = database.OpenPools(t.Context(), cfg)
	if err == nil {
		invalidPools.Close()
		t.Fatal("OpenPools() accepted a tenant login with platform privileges")
	}

	if _, err := admin.Exec(t.Context(), "GRANT ajiasu_app TO ajiasu_test_platform"); err != nil {
		t.Fatalf("grant application role to platform login: %v", err)
	}
	invalidPools, err = database.OpenPools(t.Context(), cfg)
	if err == nil {
		invalidPools.Close()
		t.Fatal("OpenPools() accepted a platform login with tenant privileges")
	}
}
