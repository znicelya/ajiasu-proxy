package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const migrationLockName = "ajiasu-compose-migration-v1"

var errMigrationLockTimeout = errors.New("migration lock timeout")

type migrationStatus struct {
	SchemaVersion          int64  `json:"schema_version"`
	SupportedSchemaVersion int64  `json:"supported_schema_version"`
	State                  string `json:"state"`
}

func executeMigration(ctx context.Context, cfg config.Migration, action string) (migrationStatus, error) {
	if action != "up" && action != "status" {
		return migrationStatus{}, fmt.Errorf("invalid migration action")
	}
	if info, err := os.Stat(cfg.Directory); err != nil || !info.IsDir() {
		return migrationStatus{}, fmt.Errorf("migration source unavailable")
	}
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return migrationStatus{}, fmt.Errorf("migration database unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return migrationStatus{}, fmt.Errorf("migration database unavailable")
	}

	if action == "up" {
		if err := acquireMigrationLock(ctx, db); err != nil {
			return migrationStatus{}, err
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = db.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, migrationLockName)
		}()
		provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS(cfg.Directory))
		if err != nil {
			return migrationStatus{}, fmt.Errorf("migration provider unavailable")
		}
		if _, err := provider.Up(ctx); err != nil {
			return migrationStatus{}, fmt.Errorf("migration execution failed")
		}
	}

	version, err := currentSchemaVersion(ctx, db)
	if err != nil {
		return migrationStatus{}, err
	}
	state := "current"
	if version < supportedSchemaVersion {
		state = "pending"
	} else if version > supportedSchemaVersion {
		state = "incompatible"
	}
	status := migrationStatus{SchemaVersion: version, SupportedSchemaVersion: supportedSchemaVersion, State: state}
	if action == "up" && state != "current" {
		return status, fmt.Errorf("migration schema verification failed")
	}
	return status, nil
}

func acquireMigrationLock(ctx context.Context, db *sql.DB) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var acquired bool
		if err := db.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, migrationLockName).Scan(&acquired); err != nil {
			if ctx.Err() != nil {
				return errMigrationLockTimeout
			}
			return fmt.Errorf("migration lock unavailable")
		}
		if acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return errMigrationLockTimeout
		case <-ticker.C:
		}
	}
}

func currentSchemaVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var tableExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.goose_db_version') IS NOT NULL`).Scan(&tableExists); err != nil {
		return 0, fmt.Errorf("inspect migration state")
	}
	if !tableExists {
		return 0, nil
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0) FROM public.goose_db_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("inspect migration version")
	}
	return version, nil
}
