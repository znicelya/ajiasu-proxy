package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
)

func TestLifecycleVersionIsLocalAndSecretFree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, exitCode := runLifecycleCLIWith([]string{"version"}, func(string) (string, bool) {
		t.Fatal("version loaded configuration")
		return "", false
	}, &stdout, &stderr, lifecycleDependencies{})
	if !handled || exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("version handled=%t exit=%d stdout=%q stderr=%q", handled, exitCode, stdout.String(), stderr.String())
	}
	for _, required := range []string{`"component":"control-plane"`, `"schema_version":11`} {
		if !strings.Contains(stdout.String(), required) {
			t.Errorf("version output missing %s: %s", required, stdout.String())
		}
	}
}

func TestLifecycleHealthUsesBoundedLocalHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			t.Errorf("health path=%q", request.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	env := map[string]string{"AJIASU_HEALTH_ENDPOINT": server.URL, "AJIASU_HEALTH_TIMEOUT": "1s"}
	lookup := func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	var stdout, stderr bytes.Buffer
	handled, exitCode := runLifecycleCLI([]string{"health", "ready"}, lookup, &stdout, &stderr)
	if !handled || exitCode != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"status":"ok"`) {
		t.Fatalf("health handled=%t exit=%d stdout=%q stderr=%q", handled, exitCode, stdout.String(), stderr.String())
	}
}

func TestLifecycleMigrationErrorsDoNotExposeSecretPath(t *testing.T) {
	const canary = "migration-path-canary"
	env := map[string]string{
		"AJIASU_ENVIRONMENT":                 "production",
		"AJIASU_DATABASE_MIGRATION_DSN_FILE": filepath.Join(t.TempDir(), canary),
	}
	lookup := func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	var stdout, stderr bytes.Buffer
	handled, exitCode := runLifecycleCLI([]string{"migrate", "status"}, lookup, &stdout, &stderr)
	if !handled || exitCode != 1 {
		t.Fatalf("migrate handled=%t exit=%d", handled, exitCode)
	}
	if strings.Contains(stdout.String(), canary) || strings.Contains(stderr.String(), canary) {
		t.Fatalf("migration output exposed path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestLifecycleMigrationStatusExitCode(t *testing.T) {
	dsnFile := filepath.Join(t.TempDir(), "migration-dsn")
	if err := writePrivateTestFile(dsnFile, "postgres://owner:secret@localhost/db"); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"AJIASU_ENVIRONMENT":                 "development",
		"AJIASU_DATABASE_MIGRATION_DSN_FILE": dsnFile,
		"AJIASU_MIGRATIONS_DIRECTORY":        repositoryMigrationDirectory(t),
		"AJIASU_MIGRATION_TIMEOUT":           "1s",
	}
	lookup := func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	dependencies := lifecycleDependencies{
		migration: func(ctx context.Context, cfg config.Migration, action string) (migrationStatus, error) {
			if action != "status" || cfg.Timeout != time.Second {
				t.Fatalf("migration action=%q cfg=%#v", action, cfg)
			}
			return migrationStatus{SchemaVersion: 10, SupportedSchemaVersion: 11, State: "pending"}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	_, exitCode := runLifecycleCLIWith([]string{"migrate", "status"}, lookup, &stdout, &stderr, dependencies)
	if exitCode != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"state":"pending"`) {
		t.Fatalf("migrate exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func repositoryMigrationDirectory(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate lifecycle test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "migrations"))
}

func writePrivateTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
