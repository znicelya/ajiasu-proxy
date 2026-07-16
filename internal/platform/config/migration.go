package config

import (
	"fmt"
	"path/filepath"
	"time"
)

type Migration struct {
	DSN       string
	Directory string
	Timeout   time.Duration
}

func LoadMigration(lookup func(string) (string, bool)) (Migration, error) {
	if lookup == nil {
		return Migration{}, fmt.Errorf("configuration lookup is required")
	}
	l := loader{lookup: lookup}
	environmentValue, err := l.required("AJIASU_ENVIRONMENT")
	if err != nil {
		return Migration{}, err
	}
	environment := Environment(environmentValue)
	if environment != EnvironmentDevelopment && environment != EnvironmentProduction {
		return Migration{}, fieldError("AJIASU_ENVIRONMENT", "must be development or production")
	}
	dsn, err := l.secretText(
		"AJIASU_DATABASE_MIGRATION_DSN",
		"AJIASU_DATABASE_MIGRATION_DSN_FILE",
		environment == EnvironmentProduction,
		64*1024,
	)
	if err != nil {
		return Migration{}, err
	}
	directory, ok := lookup("AJIASU_MIGRATIONS_DIRECTORY")
	if !ok || directory == "" {
		directory = "/usr/share/ajiasu/migrations"
	}
	if !filepath.IsAbs(directory) {
		return Migration{}, fieldError("AJIASU_MIGRATIONS_DIRECTORY", "must be absolute")
	}
	timeout := 2 * time.Minute
	if value, ok := lookup("AJIASU_MIGRATION_TIMEOUT"); ok && value != "" {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout < time.Second || timeout > 10*time.Minute {
			return Migration{}, fieldError("AJIASU_MIGRATION_TIMEOUT", "must be between 1s and 10m")
		}
	}
	return Migration{DSN: dsn, Directory: directory, Timeout: timeout}, nil
}
