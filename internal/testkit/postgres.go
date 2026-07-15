package testkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	postgresPort      = "5432/tcp"
	adminRole         = "postgres"
	tenantLoginRole   = "ajiasu_test_tenant"
	platformLoginRole = "ajiasu_test_platform"
	testDatabase      = "ajiasu_test"
)

type Postgres struct {
	AdminDSN    string
	TenantDSN   string
	PlatformDSN string
	container   testcontainers.Container
}

func StartPostgres(t *testing.T) *Postgres {
	t.Helper()
	ctx := t.Context()
	ensureDocker(t, ctx)

	adminPassword := randomPassword(t)
	tenantPassword := randomPassword(t)
	platformPassword := randomPassword(t)
	var container testcontainers.Container
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		container, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        lockedPostgresImage(t),
				ExposedPorts: []string{postgresPort},
				Env: map[string]string{
					"POSTGRES_DB":       testDatabase,
					"POSTGRES_PASSWORD": adminPassword,
					"POSTGRES_USER":     adminRole,
				},
				WaitingFor: wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(2 * time.Minute),
			},
			Started: true,
		})
		if err == nil {
			break
		}
		message := strings.ToLower(err.Error())
		if attempt == 3 || !strings.Contains(message, "reaper") && !strings.Contains(message, "removing") {
			break
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		t.Fatalf("start locked PostgreSQL container: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("resolve PostgreSQL host: %v", err)
	}
	port, err := container.MappedPort(ctx, postgresPort)
	if err != nil {
		t.Fatalf("resolve PostgreSQL port: %v", err)
	}

	postgres := &Postgres{
		AdminDSN:    postgresDSN(host, port.Port(), adminRole, adminPassword),
		TenantDSN:   postgresDSN(host, port.Port(), tenantLoginRole, tenantPassword),
		PlatformDSN: postgresDSN(host, port.Port(), platformLoginRole, platformPassword),
		container:   container,
	}
	waitForDatabase(t, ctx, postgres.AdminDSN)
	createLoginRole(t, ctx, postgres.AdminDSN, tenantLoginRole, tenantPassword)
	createLoginRole(t, ctx, postgres.AdminDSN, platformLoginRole, platformPassword)
	return postgres
}

func (p *Postgres) Restart(t *testing.T) {
	t.Helper()
	if p == nil || p.container == nil {
		t.Fatal("PostgreSQL test container is not available")
	}
	if err := p.container.Stop(t.Context(), nil); err != nil {
		t.Fatalf("stop locked PostgreSQL container: %v", err)
	}
	if err := p.container.Start(t.Context()); err != nil {
		t.Fatalf("restart locked PostgreSQL container: %v", err)
	}
	host, err := p.container.Host(t.Context())
	if err != nil {
		t.Fatalf("resolve restarted PostgreSQL host: %v", err)
	}
	port, err := p.container.MappedPort(t.Context(), postgresPort)
	if err != nil {
		t.Fatalf("resolve restarted PostgreSQL port: %v", err)
	}
	p.AdminDSN = replaceDSNHost(t, p.AdminDSN, host, port.Port())
	p.TenantDSN = replaceDSNHost(t, p.TenantDSN, host, port.Port())
	p.PlatformDSN = replaceDSNHost(t, p.PlatformDSN, host, port.Port())
	waitForDatabase(t, t.Context(), p.AdminDSN)
}

func replaceDSNHost(t *testing.T, dsn, host, port string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN during restart: %v", err)
	}
	parsed.Host = host + ":" + port
	return parsed.String()
}

func (p *Postgres) GrantApplicationRoles(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), p.AdminDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL admin pool: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(t.Context(), "GRANT ajiasu_app TO "+pgx.Identifier{tenantLoginRole}.Sanitize()); err != nil {
		t.Fatalf("grant application role: %v", err)
	}
	if _, err := pool.Exec(t.Context(), "GRANT ajiasu_platform TO "+pgx.Identifier{platformLoginRole}.Sanitize()); err != nil {
		t.Fatalf("grant platform role: %v", err)
	}
}

func ensureDocker(t *testing.T, ctx context.Context) {
	t.Helper()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		if dockerRequired() {
			t.Fatalf("Docker provider is required for PostgreSQL integration tests: %v", err)
		}
		t.Skip("BLOCKED: Docker provider is unavailable; PostgreSQL integration behavior is not mocked")
	}
	defer provider.Close()
	if err := provider.Health(ctx); err != nil {
		if dockerRequired() {
			t.Fatalf("Docker daemon is required for PostgreSQL integration tests: %v", err)
		}
		t.Skip("BLOCKED: Docker daemon is unavailable; PostgreSQL integration behavior is not mocked")
	}
}

func dockerRequired() bool {
	return os.Getenv("AJIASU_REQUIRE_DOCKER") == "1"
}

func lockedPostgresImage(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate testkit source")
	}
	lockPath := filepath.Join(filepath.Dir(source), "..", "..", "build", "control-plane-images.lock")
	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read control-plane image lock: %v", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		if image, ok := strings.CutPrefix(line, "POSTGRES_IMAGE="); ok {
			if image == "" || !strings.Contains(image, "@sha256:") {
				t.Fatal("invalid POSTGRES_IMAGE lock")
			}
			return image
		}
	}
	t.Fatal("POSTGRES_IMAGE is missing from control-plane image lock")
	return ""
}

func randomPassword(t *testing.T) string {
	t.Helper()
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("generate PostgreSQL test password: %v", err)
	}
	return hex.EncodeToString(bytes)
}

func postgresDSN(host, port, username, password string) string {
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(username, password),
		Host:     host + ":" + port,
		Path:     testDatabase,
		RawQuery: url.Values{"sslmode": []string{"disable"}}.Encode(),
	}).String()
}

func waitForDatabase(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			pool.Close()
		}
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("locked PostgreSQL container did not become queryable")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func createLoginRole(t *testing.T, ctx context.Context, adminDSN, role, password string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL admin pool: %v", err)
	}
	defer pool.Close()
	statement := fmt.Sprintf(
		"CREATE ROLE %s LOGIN PASSWORD %s NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOBYPASSRLS",
		pgx.Identifier{role}.Sanitize(),
		quoteLiteral(password),
	)
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatalf("create PostgreSQL test login role: %v", err)
	}
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
