package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationUpStatusAndDatabaseRestart(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	cfg := config.Migration{DSN: postgres.AdminDSN, Directory: repositoryMigrationDirectory(t), Timeout: 2 * time.Minute}
	status, err := executeMigration(t.Context(), cfg, "up")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "current" || status.SchemaVersion != supportedSchemaVersion {
		t.Fatalf("migration status=%#v", status)
	}
	status, err = executeMigration(t.Context(), cfg, "status")
	if err != nil || status.State != "current" {
		t.Fatalf("status before restart=%#v error=%v", status, err)
	}
	postgres.Restart(t)
	cfg.DSN = postgres.AdminDSN
	status, err = executeMigration(t.Context(), cfg, "status")
	if err != nil || status.State != "current" || status.SchemaVersion != supportedSchemaVersion {
		t.Fatalf("status after restart=%#v error=%v", status, err)
	}
}

func TestMigrationAdvisoryLockHonorsTimeout(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	db, err := sql.Open("pgx", postgres.AdminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(t.Context(), `SELECT pg_advisory_lock(hashtextextended($1, 0))`, migrationLockName); err != nil {
		t.Fatal(err)
	}
	defer connection.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, migrationLockName)

	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	_, err = executeMigration(ctx, config.Migration{DSN: postgres.AdminDSN, Directory: repositoryMigrationDirectory(t), Timeout: time.Second}, "up")
	if !errors.Is(err, errMigrationLockTimeout) {
		t.Fatalf("executeMigration() error=%v", err)
	}
}
