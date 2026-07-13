package config_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
)

func TestLoadRejectsMissingNormalDatabaseDSN(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_DATABASE_NORMAL_DSN"] = ""
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_DATABASE_NORMAL_DSN")
}

func TestLoadRejectsMissingPlatformDatabaseDSN(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_DATABASE_PLATFORM_DSN"] = ""
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_DATABASE_PLATFORM_DSN")
}

func TestLoadRejectsInsecureProductionCookie(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_ENVIRONMENT"] = "production"
	env["AJIASU_SESSION_COOKIE_SECURE"] = "false"
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_SESSION_COOKIE_SECURE")
}

func TestLoadRejectsInvalidKeyringFile(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) string
	}{
		{name: "missing", setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.key") }},
		{name: "directory", setup: func(t *testing.T) string { return t.TempDir() }},
		{name: "wrong size", setup: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "keyring")
			if err := os.WriteFile(path, []byte("too-short"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvironment(t)
			env["AJIASU_KEYRING_FILE"] = tt.setup(t)
			applyEnvironment(t, env)

			_, err := config.Load(os.LookupEnv)
			assertErrorNamesField(t, err, "AJIASU_KEYRING_FILE")
		})
	}
}

func TestLoadAcceptsExplicitDevelopmentConfiguration(t *testing.T) {
	env := validEnvironment(t)
	applyEnvironment(t, env)

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Environment != config.EnvironmentDevelopment {
		t.Fatalf("Environment = %q", cfg.Environment)
	}
	if cfg.HTTP.Bind != "127.0.0.1:8080" || cfg.HTTP.ShutdownTimeout != 7*time.Second {
		t.Fatalf("HTTP = %#v", cfg.HTTP)
	}
	if cfg.Database.Normal.MaxOpenConnections != 8 || cfg.Database.Platform.MaxIdleConnections != 3 {
		t.Fatalf("Database = %#v", cfg.Database)
	}
	if !cfg.LocalAuth.Enabled || len(cfg.LocalAuth.AllowedCIDRs) != 2 {
		t.Fatalf("LocalAuth = %#v", cfg.LocalAuth)
	}
}

func TestConfigLogValueRedactsSecrets(t *testing.T) {
	env := validEnvironment(t)
	applyEnvironment(t, env)
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}

	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "config", 0)
	record.AddAttrs(slog.Any("config", cfg))
	var output strings.Builder
	handler := slog.NewJSONHandler(&output, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return attr
	}})
	if err := handler.Handle(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output.String()), &decoded); err != nil {
		t.Fatalf("invalid JSON log: %v", err)
	}

	for _, secret := range []string{"normal-password", "platform-password", "oidc-secret", "session-cookie", "0123456789abcdef0123456789abcdef"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("log contains secret %q: %s", secret, output.String())
		}
	}
	for _, safeField := range []string{"development", "127.0.0.1:8080", "AJIASU_OIDC_CLIENT_SECRET_FILE"} {
		if !strings.Contains(output.String(), safeField) {
			t.Errorf("log does not contain safe value %q: %s", safeField, output.String())
		}
	}
}

func validEnvironment(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	keyring := filepath.Join(dir, "keyring")
	if err := os.WriteFile(keyring, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	clientSecret := filepath.Join(dir, "oidc-secret")
	if err := os.WriteFile(clientSecret, []byte("oidc-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	return map[string]string{
		"AJIASU_ENVIRONMENT":                "development",
		"AJIASU_HTTP_BIND":                  "127.0.0.1:8080",
		"AJIASU_HTTP_READ_HEADER_TIMEOUT":   "2s",
		"AJIASU_HTTP_READ_TIMEOUT":          "3s",
		"AJIASU_HTTP_WRITE_TIMEOUT":         "4s",
		"AJIASU_HTTP_IDLE_TIMEOUT":          "5s",
		"AJIASU_HTTP_SHUTDOWN_TIMEOUT":      "7s",
		"AJIASU_DATABASE_NORMAL_DSN":        "postgres://normal:normal-password@localhost/normal",
		"AJIASU_DATABASE_NORMAL_MAX_OPEN":   "8",
		"AJIASU_DATABASE_NORMAL_MAX_IDLE":   "4",
		"AJIASU_DATABASE_PLATFORM_DSN":      "postgres://platform:platform-password@localhost/platform",
		"AJIASU_DATABASE_PLATFORM_MAX_OPEN": "6",
		"AJIASU_DATABASE_PLATFORM_MAX_IDLE": "3",
		"AJIASU_OIDC_ISSUER":                "https://issuer.example.test",
		"AJIASU_OIDC_CLIENT_ID":             "control-plane",
		"AJIASU_OIDC_CLIENT_SECRET_FILE":    clientSecret,
		"AJIASU_OIDC_REDIRECT_URL":          "https://proxy.example.test/callback",
		"AJIASU_SESSION_COOKIE_NAME":        "session-cookie",
		"AJIASU_SESSION_COOKIE_SECURE":      "false",
		"AJIASU_SESSION_IDLE_TIMEOUT":       "30m",
		"AJIASU_SESSION_ABSOLUTE_TIMEOUT":   "12h",
		"AJIASU_KEYRING_FILE":               keyring,
		"AJIASU_LOCAL_AUTH_ENABLED":         "true",
		"AJIASU_LOCAL_AUTH_ALLOWED_CIDRS":   "127.0.0.0/8,::1/128",
	}
}

func applyEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func assertErrorNamesField(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Load() error = nil, want field %s", field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("Load() error = %q, want safe field name %s", err, field)
	}
}
