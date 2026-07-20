package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
)

var (
	buildVersion  = "development"
	buildRevision = "unknown"
)

type lifecycleDependencies struct {
	health    func(context.Context, string, time.Duration) error
	migration func(context.Context, config.Migration, string) (migrationStatus, error)
}

func defaultLifecycleDependencies() lifecycleDependencies {
	return lifecycleDependencies{health: executeLocalHealth, migration: executeMigration}
}

func runLifecycleCLI(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) (bool, int) {
	return runLifecycleCLIWith(args, lookup, stdout, stderr, defaultLifecycleDependencies())
}

func runLifecycleCLIWith(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer, dependencies lifecycleDependencies) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "version":
		if len(args) != 1 {
			_, _ = fmt.Fprintln(stderr, "version does not accept arguments")
			return true, 2
		}
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"component": "control-plane", "version": buildVersion, "revision": buildRevision,
			"schema_version": supportedSchemaVersion,
		})
		return true, 0
	case "health":
		if len(args) != 2 || args[1] != "live" && args[1] != "ready" {
			_, _ = fmt.Fprintln(stderr, "usage: control-plane health live|ready")
			return true, 2
		}
		endpoint, timeout, err := loadHealthCommand(lookup)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "control-plane health configuration is invalid")
			return true, 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := dependencies.health(ctx, strings.TrimRight(endpoint, "/")+"/"+args[1]+"z", timeout); err != nil {
			_, _ = fmt.Fprintln(stderr, "control-plane health check failed")
			return true, 1
		}
		_ = json.NewEncoder(stdout).Encode(map[string]string{"component": "control-plane", "check": args[1], "status": "ok"})
		return true, 0
	case "migrate":
		if len(args) != 2 || args[1] != "up" && args[1] != "status" {
			_, _ = fmt.Fprintln(stderr, "usage: control-plane migrate up|status")
			return true, 2
		}
		cfg, err := config.LoadMigration(lookup)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "control-plane migration configuration is invalid")
			return true, 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()
		status, err := dependencies.migration(ctx, cfg, args[1])
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "control-plane migration failed")
			return true, 1
		}
		_ = json.NewEncoder(stdout).Encode(status)
		if status.State != "current" {
			return true, 1
		}
		return true, 0
	default:
		return false, 0
	}
}

func loadHealthCommand(lookup func(string) (string, bool)) (string, time.Duration, error) {
	endpoint := "http://127.0.0.1:8080"
	if value, ok := lookup("AJIASU_HEALTH_ENDPOINT"); ok && strings.TrimSpace(value) != "" {
		endpoint = strings.TrimSpace(value)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, fmt.Errorf("invalid health endpoint")
	}
	timeout := 2 * time.Second
	if value, ok := lookup("AJIASU_HEALTH_TIMEOUT"); ok && strings.TrimSpace(value) != "" {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout < 100*time.Millisecond || timeout > 30*time.Second {
			return "", 0, fmt.Errorf("invalid health timeout")
		}
	}
	return endpoint, timeout, nil
}

func executeLocalHealth(ctx context.Context, endpoint string, timeout time.Duration) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected health status")
	}
	return nil
}
