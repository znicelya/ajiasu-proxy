package config_test

import (
	"encoding/json"
	"fmt"
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

func TestLoadRejectsInvalidOIDCURLs(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "relative issuer", field: "AJIASU_OIDC_ISSUER", value: "/tenant"},
		{name: "issuer without host", field: "AJIASU_OIDC_ISSUER", value: "https:///tenant"},
		{name: "unsupported issuer scheme", field: "AJIASU_OIDC_ISSUER", value: "ftp://issuer.example.test"},
		{name: "relative redirect", field: "AJIASU_OIDC_REDIRECT_URL", value: "/callback"},
		{name: "redirect without host", field: "AJIASU_OIDC_REDIRECT_URL", value: "https:///callback"},
		{name: "unsupported redirect scheme", field: "AJIASU_OIDC_REDIRECT_URL", value: "javascript://callback.example.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvironment(t)
			env[tt.field] = tt.value
			applyEnvironment(t, env)

			_, err := config.Load(os.LookupEnv)
			assertErrorNamesField(t, err, tt.field)
		})
	}
}

func TestLoadRejectsInsecureProductionOIDCURLs(t *testing.T) {
	for _, field := range []string{"AJIASU_OIDC_ISSUER", "AJIASU_OIDC_REDIRECT_URL"} {
		t.Run(field, func(t *testing.T) {
			env := validEnvironment(t)
			env["AJIASU_ENVIRONMENT"] = "production"
			env["AJIASU_SESSION_COOKIE_SECURE"] = "true"
			env[field] = "http://oidc.example.test/path"
			applyEnvironment(t, env)

			_, err := config.Load(os.LookupEnv)
			assertErrorNamesField(t, err, field)
		})
	}
}

func TestLoadRejectsInvalidCIDR(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_LOCAL_AUTH_ALLOWED_CIDRS"] = "127.0.0.0/8,not-a-cidr"
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_LOCAL_AUTH_ALLOWED_CIDRS")
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_HTTP_READ_TIMEOUT"] = "eventually"
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_HTTP_READ_TIMEOUT")
}

func TestLoadRejectsMissingOIDCClientSecret(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_OIDC_CLIENT_SECRET_FILE"] = ""
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_OIDC_CLIENT_SECRET_FILE")
}

func TestLoadErrorsDoNotEchoInvalidValues(t *testing.T) {
	const canary = "secret-url-canary-7f9b1"
	env := validEnvironment(t)
	env["AJIASU_OIDC_ISSUER"] = "://" + canary
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_OIDC_ISSUER")
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("Load() error exposed invalid value: %q", err)
	}
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

func TestConfigStringRedactsSecrets(t *testing.T) {
	env := validEnvironment(t)
	applyEnvironment(t, env)
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}

	for _, formatted := range []string{formatAsString(cfg), fmt.Sprintf("%v", cfg)} {
		for _, secret := range []string{
			"normal-password",
			"platform-password",
			cfg.OIDC.ClientSecretFile,
			"session-cookie",
			cfg.KeyringFile,
			"0123456789abcdef0123456789abcdef",
		} {
			if strings.Contains(formatted, secret) {
				t.Errorf("Config string contains secret %q: %s", secret, formatted)
			}
		}
	}
}

func formatAsString(value any) string {
	return fmt.Sprintf("%s", value)
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
