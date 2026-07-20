package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
)

func TestLoadMigrationUsesPrivateDSNFileAndBounds(t *testing.T) {
	dsnFile := filepath.Join(t.TempDir(), "migration-dsn")
	if err := os.WriteFile(dsnFile, []byte(" postgres://owner:secret@localhost/db \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"AJIASU_ENVIRONMENT":                 "production",
		"AJIASU_DATABASE_MIGRATION_DSN_FILE": dsnFile,
		"AJIASU_MIGRATIONS_DIRECTORY":        filepath.Clean(t.TempDir()),
		"AJIASU_MIGRATION_TIMEOUT":           "45s",
	}
	cfg, err := config.LoadMigration(func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DSN != "postgres://owner:secret@localhost/db" || cfg.Timeout != 45*time.Second {
		t.Fatalf("Migration = %#v", cfg)
	}
}

func TestLoadMigrationRejectsConflictWithoutExposingValues(t *testing.T) {
	const canary = "migration-secret-canary"
	env := map[string]string{
		"AJIASU_ENVIRONMENT":                 "development",
		"AJIASU_DATABASE_MIGRATION_DSN":      "postgres://owner:" + canary + "@localhost/db",
		"AJIASU_DATABASE_MIGRATION_DSN_FILE": filepath.Join(t.TempDir(), canary),
	}
	_, err := config.LoadMigration(func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	})
	if err == nil || !strings.Contains(err.Error(), "AJIASU_DATABASE_MIGRATION_DSN_FILE") {
		t.Fatalf("LoadMigration() error=%v", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("LoadMigration() exposed secret value or path: %v", err)
	}
}
