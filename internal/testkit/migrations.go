package testkit

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func MigrationsUp(t *testing.T, dsn string) {
	t.Helper()
	provider, db := migrationProvider(t, dsn)
	defer db.Close()
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("migrate PostgreSQL up: %v", err)
	}
}

func MigrationsDown(t *testing.T, dsn string) {
	t.Helper()
	MigrationsDownTo(t, dsn, 0)
}

func MigrationsDownTo(t *testing.T, dsn string, version int64) {
	t.Helper()
	provider, db := migrationProvider(t, dsn)
	defer db.Close()
	if _, err := provider.DownTo(t.Context(), version); err != nil {
		t.Fatalf("migrate PostgreSQL down to %d: %v", version, err)
	}
}

func migrationProvider(t *testing.T, dsn string) (*goose.Provider, *sql.DB) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL migration connection: %v", err)
	}
	if err := db.PingContext(t.Context()); err != nil {
		db.Close()
		t.Fatalf("ping PostgreSQL migration connection: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationFS(t))
	if err != nil {
		db.Close()
		t.Fatalf("create PostgreSQL migration provider: %v", err)
	}
	return provider, db
}

func migrationFS(t *testing.T) fs.FS {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration helper source")
	}
	dir := filepath.Join(filepath.Dir(source), "..", "..", "migrations")
	return os.DirFS(dir)
}
