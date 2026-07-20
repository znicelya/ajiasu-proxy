package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
)

func TestLoadRejectsMissingNormalDatabaseDSN(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_DATABASE_NORMAL_DSN_FILE"] = ""
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_DATABASE_NORMAL_DSN_FILE")
}

func TestLoadRejectsMissingPlatformDatabaseDSN(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_DATABASE_PLATFORM_DSN_FILE"] = ""
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_DATABASE_PLATFORM_DSN_FILE")
}

func TestLoadRejectsConflictingDatabaseDSNAndFile(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_DATABASE_NORMAL_DSN"] = "postgres://must-not-appear@example.invalid/db"
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_DATABASE_NORMAL_DSN_FILE")
	if strings.Contains(fmt.Sprint(err), "must-not-appear") {
		t.Fatalf("Load() exposed conflicting DSN: %v", err)
	}
}

func TestLoadRejectsDirectDatabaseDSNInProduction(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_ENVIRONMENT"] = "production"
	env["AJIASU_SESSION_COOKIE_SECURE"] = "true"
	env["AJIASU_DATABASE_NORMAL_DSN"] = "postgres://must-not-appear@example.invalid/db"
	delete(env, "AJIASU_DATABASE_NORMAL_DSN_FILE")
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	assertErrorNamesField(t, err, "AJIASU_DATABASE_NORMAL_DSN")
	if strings.Contains(fmt.Sprint(err), "must-not-appear") {
		t.Fatalf("Load() exposed direct DSN: %v", err)
	}
}

func TestLoadRejectsUnsafeDatabaseDSNFile(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
		setup   func(*testing.T, string)
	}{
		{name: "whitespace", content: []byte(" \r\n\t")},
		{name: "oversize", content: bytes.Repeat([]byte("x"), 64*1024+1)},
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target")
			if err := os.WriteFile(target, []byte("postgres://user:secret@localhost/db"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symbolic links unavailable: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := validEnvironment(t)
			path := filepath.Join(t.TempDir(), "normal-dsn")
			if test.setup != nil {
				test.setup(t, path)
			} else if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			env["AJIASU_DATABASE_NORMAL_DSN_FILE"] = path
			applyEnvironment(t, env)
			_, err := config.Load(os.LookupEnv)
			assertErrorNamesField(t, err, "AJIASU_DATABASE_NORMAL_DSN_FILE")
			if strings.Contains(fmt.Sprint(err), path) {
				t.Fatalf("Load() exposed DSN path: %v", err)
			}
		})
	}
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

func TestLoadRejectsInvalidOIDCClientSecretFile(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		useDir  bool
	}{
		{name: "empty"},
		{name: "whitespace", content: []byte(" \r\n\t")},
		{name: "directory", useDir: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvironment(t)
			path := filepath.Join(t.TempDir(), "client-secret")
			if tt.useDir {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatal(err)
			}
			env["AJIASU_OIDC_CLIENT_SECRET_FILE"] = path
			applyEnvironment(t, env)

			_, err := config.Load(os.LookupEnv)
			assertErrorNamesField(t, err, "AJIASU_OIDC_CLIENT_SECRET_FILE")
			if strings.Contains(fmt.Sprint(err), path) {
				t.Fatalf("Load() error exposed secret path: %q", err)
			}
		})
	}
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
	if cfg.Deployment.EnvironmentID != "phase7-test" {
		t.Fatalf("Deployment = %#v", cfg.Deployment)
	}
	if cfg.HTTP.Bind != "127.0.0.1:8080" || cfg.HTTP.ShutdownTimeout != 7*time.Second {
		t.Fatalf("HTTP = %#v", cfg.HTTP)
	}
	if cfg.Database.Normal.MaxOpenConnections != 8 || cfg.Database.Platform.MinIdleConnections != 3 || cfg.Database.Platform.MaxIdleConnections != 3 {
		t.Fatalf("Database = %#v", cfg.Database)
	}
	if !cfg.LocalAuth.Enabled || len(cfg.LocalAuth.AllowedCIDRs) != 2 {
		t.Fatalf("LocalAuth = %#v", cfg.LocalAuth)
	}
}

func TestLoadAcceptsLegacyMaxIdleConfiguration(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_DATABASE_NORMAL_MAX_IDLE"] = env["AJIASU_DATABASE_NORMAL_MIN_IDLE"]
	env["AJIASU_DATABASE_PLATFORM_MAX_IDLE"] = env["AJIASU_DATABASE_PLATFORM_MIN_IDLE"]
	delete(env, "AJIASU_DATABASE_NORMAL_MIN_IDLE")
	delete(env, "AJIASU_DATABASE_PLATFORM_MIN_IDLE")
	applyEnvironment(t, env)

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatalf("Load() legacy idle aliases error = %v", err)
	}
	if cfg.Database.Normal.MinIdleConnections != 4 || cfg.Database.Normal.MaxIdleConnections != 4 {
		t.Fatalf("legacy normal database pool = %#v", cfg.Database.Normal)
	}
}

func TestLoadRejectsConflictingIdleAliases(t *testing.T) {
	env := validEnvironment(t)
	env["AJIASU_DATABASE_NORMAL_MAX_IDLE"] = "3"
	applyEnvironment(t, env)

	_, err := config.Load(os.LookupEnv)
	if err == nil {
		t.Fatal("Load() accepted conflicting MIN_IDLE and MAX_IDLE aliases")
	}
	assertErrorNamesField(t, err, "AJIASU_DATABASE_NORMAL_MIN_IDLE")
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

	for _, secret := range []string{"normal-password", "platform-password", "oidc-secret", "redis-secret", "session-cookie", "0123456789abcdef0123456789abcdef"} {
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
			cfg.OIDC.ClientSecret.Text(),
			cfg.Redis.Password.Text(),
			"session-cookie",
			cfg.Keyring.Text(),
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
	routeSigningKey := filepath.Join(dir, "route-signing-key")
	if err := os.WriteFile(routeSigningKey, bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	clientSecret := filepath.Join(dir, "oidc-secret")
	if err := os.WriteFile(clientSecret, []byte("oidc-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	redisPassword := filepath.Join(dir, "redis-password")
	if err := os.WriteFile(redisPassword, []byte("redis-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	normalDSN := filepath.Join(dir, "normal-dsn")
	if err := os.WriteFile(normalDSN, []byte("postgres://normal:normal-password@localhost/normal"), 0o600); err != nil {
		t.Fatal(err)
	}
	platformDSN := filepath.Join(dir, "platform-dsn")
	if err := os.WriteFile(platformDSN, []byte("postgres://platform:platform-password@localhost/platform"), 0o600); err != nil {
		t.Fatal(err)
	}

	return map[string]string{
		"AJIASU_ENVIRONMENT":                    "development",
		"AJIASU_ENVIRONMENT_ID":                 "phase7-test",
		"AJIASU_HTTP_BIND":                      "127.0.0.1:8080",
		"AJIASU_HTTP_READ_HEADER_TIMEOUT":       "2s",
		"AJIASU_HTTP_READ_TIMEOUT":              "3s",
		"AJIASU_HTTP_WRITE_TIMEOUT":             "4s",
		"AJIASU_HTTP_IDLE_TIMEOUT":              "5s",
		"AJIASU_HTTP_SHUTDOWN_TIMEOUT":          "7s",
		"AJIASU_AGENT_GRPC_BIND":                "127.0.0.1:9090",
		"AJIASU_AGENT_GRPC_INSECURE":            "true",
		"AJIASU_GATEWAY_GRPC_BIND":              "127.0.0.1:9091",
		"AJIASU_GATEWAY_GRPC_INSECURE":          "true",
		"AJIASU_DATABASE_NORMAL_DSN_FILE":       normalDSN,
		"AJIASU_DATABASE_NORMAL_MAX_OPEN":       "8",
		"AJIASU_DATABASE_NORMAL_MIN_IDLE":       "4",
		"AJIASU_DATABASE_PLATFORM_DSN_FILE":     platformDSN,
		"AJIASU_DATABASE_PLATFORM_MAX_OPEN":     "6",
		"AJIASU_DATABASE_PLATFORM_MIN_IDLE":     "3",
		"AJIASU_REDIS_ADDRESS":                  "127.0.0.1:6379",
		"AJIASU_REDIS_USERNAME":                 "scheduler",
		"AJIASU_REDIS_PASSWORD_FILE":            redisPassword,
		"AJIASU_REDIS_DATABASE":                 "0",
		"AJIASU_REDIS_TLS":                      "false",
		"AJIASU_REDIS_OPERATION_TIMEOUT":        "1s",
		"AJIASU_SCHEDULER_LEASE_NAMESPACE":      "ajiasu:lease:v1",
		"AJIASU_SCHEDULER_LEASE_TTL":            "9s",
		"AJIASU_SCHEDULER_LEASE_RENEW_INTERVAL": "2s",
		"AJIASU_OIDC_ISSUER":                    "https://issuer.example.test",
		"AJIASU_OIDC_CLIENT_ID":                 "control-plane",
		"AJIASU_OIDC_CLIENT_SECRET_FILE":        clientSecret,
		"AJIASU_OIDC_REDIRECT_URL":              "https://proxy.example.test/callback",
		"AJIASU_SESSION_COOKIE_NAME":            "session-cookie",
		"AJIASU_SESSION_COOKIE_SECURE":          "false",
		"AJIASU_SESSION_IDLE_TIMEOUT":           "30m",
		"AJIASU_SESSION_ABSOLUTE_TIMEOUT":       "12h",
		"AJIASU_KEYRING_FILE":                   keyring,
		"AJIASU_ROUTE_SIGNING_KEY_FILE":         routeSigningKey,
		"AJIASU_LOCAL_AUTH_ENABLED":             "true",
		"AJIASU_LOCAL_AUTH_ALLOWED_CIDRS":       "127.0.0.0/8,::1/128",
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
