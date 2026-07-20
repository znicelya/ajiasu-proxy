package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/znicelya/ajiasu-proxy/internal/testkit"
)

func TestRunningControlPlaneTransitionsReadyAcrossMigration(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	cfg := config.Config{Database: config.Database{
		Normal:   config.DatabasePool{DSN: postgres.TenantDSN, MaxOpenConnections: 2},
		Platform: config.DatabasePool{DSN: postgres.PlatformDSN, MaxOpenConnections: 2},
	}}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	switcher := &switchingHandler{}
	runtime := newApplicationRuntime(cfg, logger, switcher.Store)
	runtime.build = func(_ config.Config, logger *slog.Logger, _ *database.Pools, readiness httpserver.Readiness) (http.Handler, error) {
		return httpserver.NewRouter(httpserver.Dependencies{Logger: logger, Readiness: readiness}), nil
	}
	t.Cleanup(runtime.Close)
	switcher.Store(httpserver.NewRouter(httpserver.Dependencies{Logger: logger, Readiness: runtime}))

	before := httptest.NewRecorder()
	switcher.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if before.Code != http.StatusServiceUnavailable {
		t.Fatalf("before migration status=%d body=%s", before.Code, before.Body.String())
	}

	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	if err := runtime.Check(t.Context()); err != nil {
		t.Fatalf("initialize after migration: %v", err)
	}
	after := httptest.NewRecorder()
	switcher.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if after.Code != http.StatusOK {
		t.Fatalf("after migration status=%d body=%s", after.Code, after.Body.String())
	}

	testkit.MigrationsDownTo(t, postgres.AdminDSN, supportedSchemaVersion-1)
	downgraded := httptest.NewRecorder()
	switcher.ServeHTTP(downgraded, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if downgraded.Code != http.StatusServiceUnavailable {
		t.Fatalf("downgraded schema status=%d body=%s", downgraded.Code, downgraded.Body.String())
	}
	testkit.MigrationsUp(t, postgres.AdminDSN)
	restored := httptest.NewRecorder()
	switcher.ServeHTTP(restored, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if restored.Code != http.StatusOK {
		t.Fatalf("restored schema status=%d body=%s", restored.Code, restored.Body.String())
	}
}
