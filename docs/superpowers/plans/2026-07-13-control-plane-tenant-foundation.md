# Control-Plane and Tenant Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Phase 2 Go control plane with PostgreSQL-backed multi-tenancy, RLS, identity, RBAC, audit, OIDC, break-glass authentication, service identities, and a versioned management API.

**Architecture:** A vertical-slice Go modular monolith uses `chi`, `pgx`, and `sqlc`. PostgreSQL is authoritative for business state, sessions, idempotency, audit, and Outbox state; tenant repositories require server-derived transaction context and forced RLS. Browser users authenticate through server-side sessions, while automation uses short-lived opaque service tokens.

**Tech Stack:** Go 1.25.3, chi 5.3.1, pgx 5.10.0, sqlc 1.30.0, Goose 3.26.0, PostgreSQL 17, Keycloak 26, OpenAPI 3.1, Testcontainers Go 0.43.0, OpenTelemetry 1.44.0, Prometheus client 1.23.2

---

## Execution Rules

> **Approved compatibility amendment (2026-07-13):** Keep the project at Go 1.25.0 with toolchain Go 1.25.3. Pin sqlc to v1.30.0 and Goose to v3.26.0 because sqlc v1.31.1 requires Go 1.26.0 and Goose v3.27.2 requires Go 1.25.7. This amendment preserves the approved Go build image and was explicitly selected by the user during Task 1 execution.

- Work in a new worktree on branch `feat/phase-2-control-plane`.
- Follow strict test-driven development for every behavior change.
- Do not start a later task while either specification or quality review has open Critical or Important findings.
- Preserve the full Phase 1 Runner gate: `powershell -NoProfile -File scripts/ci.ps1` must pass after every task.
- Never use real AJiaSu credentials, real OIDC production secrets, or production encryption keys.
- Use pinned module versions and digest-lock PostgreSQL, Keycloak, Go build, and control-plane runtime images before CI relies on them.

## Locked Go Modules

Initialize the module as `github.com/znicelya/ajiasu-proxy` with:

```text
go 1.25.0
toolchain go1.25.3

github.com/coreos/go-oidc/v3 v3.20.0
github.com/getkin/kin-openapi v0.142.0
github.com/go-chi/chi/v5 v5.3.1
github.com/google/uuid v1.6.0
github.com/jackc/pgx/v5 v5.10.0
github.com/pquerna/otp v1.5.0
github.com/prometheus/client_golang v1.23.2
github.com/testcontainers/testcontainers-go v0.43.0
go.opentelemetry.io/otel v1.44.0
golang.org/x/crypto v0.54.0
golang.org/x/oauth2 v0.36.0
```

Tool dependencies:

```text
github.com/pressly/goose/v3/cmd/goose v3.26.0
github.com/sqlc-dev/sqlc/cmd/sqlc v1.30.0
honnef.co/go/tools/cmd/staticcheck v0.7.0
```

If a listed version cannot resolve, stop and report the upstream/module-proxy failure. Do not silently select a different version.

## Target File Map

```text
.
├── api/openapi/control-plane.yaml
├── build/control-plane-images.lock
├── cmd/control-plane/main.go
├── cmd/control-plane/admin.go
├── internal/platform/
│   ├── config/config.go
│   ├── config/config_test.go
│   ├── database/pools.go
│   ├── database/tx.go
│   ├── database/tx_test.go
│   ├── httpserver/errors.go
│   ├── httpserver/middleware.go
│   ├── httpserver/router.go
│   ├── httpserver/server.go
│   ├── keyring/aesgcm.go
│   ├── keyring/aesgcm_test.go
│   ├── logging/logging.go
│   └── requestctx/context.go
├── internal/audit/
├── internal/identity/
├── internal/tenancy/
├── internal/testkit/
├── migrations/
├── sql/queries/
├── sql/schema/
├── sqlc.yaml
├── tools.go
├── go.mod
├── go.sum
├── Dockerfile.control-plane
├── scripts/control-plane-ci.ps1
└── .github/workflows/control-plane-ci.yml
```

Generated sqlc files live under the owning module's `internal/.../dbgen` directory. Generated files are never hand-edited.

### Task 1: Go Service Bootstrap, Configuration, Logging, and Health

**Files:**

- Create: `go.mod`
- Create: `tools.go`
- Create: `cmd/control-plane/main.go`
- Create: `internal/platform/config/config.go`
- Create: `internal/platform/config/config_test.go`
- Create: `internal/platform/logging/logging.go`
- Create: `internal/platform/requestctx/context.go`
- Create: `internal/platform/httpserver/server.go`
- Create: `internal/platform/httpserver/router.go`
- Create: `internal/platform/httpserver/middleware.go`
- Create: `internal/platform/httpserver/errors.go`
- Create: `internal/platform/httpserver/server_test.go`
- Create: `scripts/control-plane-ci.ps1`
- Modify: `scripts/ci.ps1`

- [ ] **Step 1: Initialize the pinned module**

Run:

```powershell
go mod init github.com/znicelya/ajiasu-proxy
go mod edit -go=1.25.0 -toolchain=go1.25.3
go get github.com/go-chi/chi/v5@v5.3.1
go get github.com/prometheus/client_golang@v1.23.2
go get go.opentelemetry.io/otel@v1.44.0
```

Create `tools.go`:

```go
//go:build tools

package tools

import (
	_ "github.com/pressly/goose/v3/cmd/goose"
	_ "github.com/sqlc-dev/sqlc/cmd/sqlc"
	_ "honnef.co/go/tools/cmd/staticcheck"
)
```

Pin tools:

```powershell
go get -tool github.com/pressly/goose/v3/cmd/goose@v3.26.0
go get -tool github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
go get -tool honnef.co/go/tools/cmd/staticcheck@v0.7.0
go mod tidy
```

Expected: `go.mod` contains the exact Go/toolchain directives and requested module versions.

- [ ] **Step 2: Write failing configuration tests**

Create `internal/platform/config/config_test.go` with table tests that require:

```go
func TestLoadRejectsMissingDatabaseDSN(t *testing.T)
func TestLoadRejectsProductionWithoutSecureCookie(t *testing.T)
func TestLoadRejectsMissingKeyringFile(t *testing.T)
func TestLoadAcceptsExplicitDevelopmentConfig(t *testing.T)
func TestConfigStringRedactsSecrets(t *testing.T)
```

Use `t.Setenv` and a temporary 32-byte key file. Assert rejected configurations return stable field names but never echo secret values.

Run:

```powershell
go test ./internal/platform/config -run TestLoad -v
```

Expected: FAIL because `config.Load` does not exist.

- [ ] **Step 3: Implement immutable configuration**

Create these public types in `config.go`:

```go
type Environment string

const (
	EnvironmentDevelopment Environment = "development"
	EnvironmentProduction  Environment = "production"
)

type Config struct {
	Environment Environment
	HTTP        HTTP
	Database    Database
	OIDC        OIDC
	Session     Session
	KeyringFile string
	LocalAuth   LocalAuth
}

func Load(lookup func(string) (string, bool)) (Config, error)
func (c Config) LogValue() slog.Value
```

Configuration must reject missing normal/platform DSNs, invalid URLs/CIDRs/durations, production insecure cookies, absent OIDC secrets, and a key file that is not a regular 32-byte file.

Run the tests again. Expected: PASS.

- [ ] **Step 4: Write failing HTTP middleware tests**

Create `server_test.go` to assert:

```go
func TestLivezDoesNotCallReadinessDependency(t *testing.T)
func TestReadyzReturns503WhenDatabaseIsUnavailable(t *testing.T)
func TestRequestIDIsReturnedAndLogged(t *testing.T)
func TestPanicRecoveryHidesStackFromClient(t *testing.T)
func TestBodyLimitRejectsMoreThanOneMiB(t *testing.T)
```

Run:

```powershell
go test ./internal/platform/httpserver -v
```

Expected: FAIL because the router/server do not exist.

- [ ] **Step 5: Implement HTTP foundation**

Implement:

```go
type Readiness interface {
	Check(context.Context) error
}

type Dependencies struct {
	Logger    *slog.Logger
	Readiness Readiness
}

func NewRouter(deps Dependencies) http.Handler
func NewServer(cfg config.HTTP, handler http.Handler) *http.Server
```

Middleware order is request ID, real-client IP from trusted direct peer only, panic recovery, structured access log, body limit, and route handling. `/livez` always returns 200 while the process is running. `/readyz` returns 200 or a redacted 503.

- [ ] **Step 6: Add the process entry point and local Go gate**

`main.go` must load configuration, construct JSON slog, create the HTTP server, handle `SIGINT`/`SIGTERM`, and use the configured graceful-shutdown deadline.

Create `scripts/control-plane-ci.ps1` to run, with explicit native exit checks:

```powershell
go mod tidy
git diff --exit-code -- go.mod go.sum
go test -race ./...
go vet ./...
go tool staticcheck ./...
```

Modify `scripts/ci.ps1` to invoke `scripts/control-plane-ci.ps1` after the Phase 1 gates, using the current PowerShell process.

- [ ] **Step 7: Verify and commit**

```powershell
powershell -NoProfile -File scripts/control-plane-ci.ps1
powershell -NoProfile -File scripts/ci.ps1
git diff --check
git add go.mod go.sum tools.go cmd internal/platform scripts
git commit -m "feat: bootstrap control-plane service"
```

### Task 2: PostgreSQL, Goose, sqlc, Transaction Context, and RLS Foundation

**Files:**

- Create: `build/control-plane-images.lock`
- Create: `scripts/lock-control-plane-images.ps1`
- Create: `sqlc.yaml`
- Create: `migrations/00001_platform_foundation.sql`
- Create: `internal/platform/database/pools.go`
- Create: `internal/platform/database/tx.go`
- Create: `internal/platform/database/tx_test.go`
- Create: `internal/testkit/postgres.go`
- Create: `internal/testkit/migrations.go`

- [ ] **Step 1: Lock test images**

Resolve immutable top-level multiarch digests for versioned tags:

```text
postgres:17.6-alpine3.22
quay.io/keycloak/keycloak:26.3.2
golang:1.25.3-alpine3.22
```

`scripts/lock-control-plane-images.ps1` must validate active amd64 and arm64 manifests and atomically write:

```text
POSTGRES_IMAGE=postgres:17.6-alpine3.22@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94
KEYCLOAK_IMAGE=quay.io/keycloak/keycloak:26.3.2@sha256:98fab020a3a490aba0978f237e2a06cd0ea42bf149c6cf10f11c0aaf27728ff2
GO_BUILD_IMAGE=golang:1.25.3-alpine3.22@sha256:aee43c3ccbf24fdffb7295693b6e33b21e01baec1b2a55acc351fde345e9ec34
```

If a tag is unavailable, stop and choose a published patch version through a reviewed plan amendment; do not substitute `latest`.

- [ ] **Step 2: Write failing migration tests**

Use Testcontainers to start the locked PostgreSQL image. Tests must execute:

```go
func TestMigrationsUpDownUp(t *testing.T)
func TestApplicationRoleCannotBypassRLS(t *testing.T)
func TestTransactionLocalTenantContextDoesNotLeak(t *testing.T)
```

Expected RED: migration directory/configuration is absent.

- [ ] **Step 3: Add the platform migration**

`00001_platform_foundation.sql` creates schemas `platform`, `identity`, `tenancy`, and `audit`; NOLOGIN roles `ajiasu_app` and `ajiasu_platform`; functions:

```sql
CREATE FUNCTION platform.current_tenant_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;

CREATE FUNCTION platform.current_actor_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.actor_id', true), '')::uuid
$$;
```

Down migration drops the functions, schemas, and group roles in reverse dependency order.

- [ ] **Step 4: Configure sqlc and database pools**

`sqlc.yaml` uses PostgreSQL engine, pgx/v5 output, UUID/time mappings, prepared queries disabled, and per-module output directories.

Implement:

```go
type Pools struct {
	Tenant   *pgxpool.Pool
	Platform *pgxpool.Pool
}

func OpenPools(ctx context.Context, cfg config.Database) (*Pools, error)

type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func InTenantTx[T any](ctx context.Context, pool *pgxpool.Pool, tenantID, actorID uuid.UUID, fn func(context.Context, pgx.Tx) (T, error)) (T, error)
func InPlatformTx[T any](ctx context.Context, pool *pgxpool.Pool, actorID uuid.UUID, fn func(context.Context, pgx.Tx) (T, error)) (T, error)
```

`InTenantTx` uses `set_config('app.tenant_id', $1, true)` and `set_config('app.actor_id', $1, true)` inside the transaction, rolls back on every error, and never retries unknown failures.

- [ ] **Step 5: Verify connection reuse**

Tests acquire the same pooled connection after commit and rollback and prove `current_setting('app.tenant_id', true)` is empty. Also prove the tenant role cannot select an RLS-protected fixture row without context.

- [ ] **Step 6: Verify and commit**

```powershell
go tool goose -dir migrations postgres $env:TEST_DATABASE_URL up
go tool goose -dir migrations postgres $env:TEST_DATABASE_URL down
go tool goose -dir migrations postgres $env:TEST_DATABASE_URL up
go tool sqlc generate
go test -race ./internal/platform/database ./internal/testkit -v
powershell -NoProfile -File scripts/ci.ps1
git add build scripts/lock-control-plane-images.ps1 sqlc.yaml migrations internal/platform/database internal/testkit
git commit -m "feat: add postgres transaction foundation"
```

### Task 3: Append-Only Audit and Transaction Outbox

**Files:**

- Create: `migrations/00002_audit_outbox.sql`
- Create: `sql/queries/audit.sql`
- Create: `internal/audit/model.go`
- Create: `internal/audit/repository.go`
- Create: `internal/audit/service.go`
- Create: `internal/audit/service_test.go`
- Create: `internal/audit/integration_test.go`

- [ ] **Step 1: Write failing audit atomicity tests**

Tests require:

```go
func TestAppendStoresWhitelistedAuditAndOutboxAtomically(t *testing.T)
func TestAuditFailureRollsBackBusinessTransaction(t *testing.T)
func TestApplicationRoleCannotUpdateOrDeleteAudit(t *testing.T)
func TestOutboxLeaseUsesSkipLocked(t *testing.T)
```

Expected RED: tables and service do not exist.

- [ ] **Step 2: Add audit/outbox migration**

Create `audit.audit_events` with actor type/ID, nullable tenant, action, resource type/ID, result, source IP, user agent, request ID, JSONB details, and created time. Create `platform.outbox_events` with event type, aggregate, payload version/JSONB, available time, lease owner/deadline, attempts, and processed time.

Enable forced RLS on tenant audit rows. Grant insert/select only. Add a trigger function that raises SQLSTATE `55000` for update/delete on audit rows.

- [ ] **Step 3: Implement the audit service**

```go
type Event struct {
	ActorType   string
	ActorID     *uuid.UUID
	TenantID    *uuid.UUID
	Action      string
	ResourceType string
	ResourceID  *uuid.UUID
	Result      string
	SourceIP    netip.Addr
	UserAgent   string
	RequestID   uuid.UUID
	Details     map[string]any
}

type Service interface {
	Append(context.Context, database.Executor, Event, OutboxEvent) error
}
```

Reject detail keys matching password, secret, token, authorization, cookie, recovery, or totp before SQL execution.

- [ ] **Step 4: Add Outbox leasing**

Lease SQL uses `FOR UPDATE SKIP LOCKED`, a bounded batch, lease owner/deadline, and deterministic ordering. Completing or releasing a lease must match the current lease owner.

- [ ] **Step 5: Verify and commit**

```powershell
go tool sqlc generate
go test -race ./internal/audit -v
powershell -NoProfile -File scripts/ci.ps1
git add migrations/00002_audit_outbox.sql sql/queries/audit.sql internal/audit
git commit -m "feat: add append-only audit and outbox"
```

### Task 4: Tenants, Memberships, Fixed Roles, and Isolation

**Files:**

- Create: `migrations/00003_tenancy.sql`
- Create: `sql/queries/tenancy.sql`
- Create: `internal/tenancy/model.go`
- Create: `internal/tenancy/repository.go`
- Create: `internal/tenancy/policy.go`
- Create: `internal/tenancy/service.go`
- Create: `internal/tenancy/service_test.go`
- Create: `internal/tenancy/isolation_test.go`

- [ ] **Step 1: Write failing domain and policy tests**

Cover fixed roles, default deny, platform role separation, membership removal, tenant suspension, version conflict, and URL tenant escalation.

Representative assertion:

```go
func TestPolicyDeniesTenantAdminFromAnotherTenant(t *testing.T) {
	decision := policy.Authorize(Subject{TenantIDs: []uuid.UUID{tenantA}, Roles: []Role{TenantAdmin}}, ManageMembers, tenantB)
	if decision.Allowed { t.Fatal("cross-tenant authorization allowed") }
}
```

- [ ] **Step 2: Add tenancy migration and RLS**

Create `tenancy.tenants`, `tenancy.memberships`, and `tenancy.role_bindings`. Add checks for lifecycle and fixed roles, unique membership/role constraints, version columns, indexes, forced RLS, and policies based on `platform.current_tenant_id()`.

Platform administrators create/update tenants through the platform pool. Tenant member/role operations use tenant transactions.

- [ ] **Step 3: Implement repository and services**

Public methods:

```go
func (s *Service) CreateTenant(context.Context, PlatformActor, CreateTenant) (Tenant, error)
func (s *Service) UpdateTenant(context.Context, PlatformActor, UpdateTenant) (Tenant, error)
func (s *Service) AddMember(context.Context, TenantActor, AddMember) (Membership, error)
func (s *Service) RemoveMember(context.Context, TenantActor, uuid.UUID) error
func (s *Service) GrantRole(context.Context, TenantActor, GrantRole) (RoleBinding, error)
func (s *Service) RevokeRole(context.Context, TenantActor, uuid.UUID) error
```

Every write appends audit and Outbox in the same transaction. Stale versions map to `ErrVersionConflict`.

- [ ] **Step 4: Prove cross-tenant isolation**

Integration tests create Tenant A and Tenant B, reuse pooled connections, and attempt select/update/delete on memberships and role bindings through the wrong tenant context. Every attempt must return no row or an RLS error and leave data unchanged.

- [ ] **Step 5: Verify and commit**

```powershell
go tool sqlc generate
go test -race ./internal/tenancy -v
powershell -NoProfile -File scripts/ci.ps1
git add migrations/00003_tenancy.sql sql/queries/tenancy.sql internal/tenancy
git commit -m "feat: add tenant rbac foundation"
```

### Task 5: Keyring and Break-Glass Administrator

**Files:**

- Create: `migrations/00004_local_identity.sql`
- Create: `sql/queries/local_identity.sql`
- Create: `internal/platform/keyring/aesgcm.go`
- Create: `internal/platform/keyring/aesgcm_test.go`
- Create: `internal/identity/password.go`
- Create: `internal/identity/totp.go`
- Create: `internal/identity/local.go`
- Create: `internal/identity/local_test.go`
- Create: `cmd/control-plane/admin.go`

- [ ] **Step 1: Write failing Keyring tests**

Test 32-byte key enforcement, independent nonces, authenticated-context mismatch, tamper rejection, and absence of plaintext in ciphertext.

Implement:

```go
type Keyring interface {
	Encrypt(plaintext, additionalData []byte) ([]byte, error)
	Decrypt(ciphertext, additionalData []byte) ([]byte, error)
}
```

AES-256-GCM output format is version byte, nonce, then sealed ciphertext.

- [ ] **Step 2: Write failing password/TOTP/bootstrap tests**

Cover Argon2id parameters, password mismatch, valid/invalid TOTP, encrypted secret, one-time recovery codes, bootstrap refusal when an admin exists, source CIDR rejection, lockout, and audit on success/failure.

Use Argon2id parameters encoded with the verifier so future upgrades remain possible. Comparison is constant-time.

- [ ] **Step 3: Add local identity migration**

Create global identity/local-admin/recovery-code/login-attempt tables. Local admins are platform-scoped. Store encrypted TOTP bytes, password verifier, disabled/lock timestamps, and versions. Never store recovery-code plaintext.

- [ ] **Step 4: Implement interactive bootstrap**

`control-plane admin bootstrap` reads password/TOTP confirmation from a terminal without echo, generates recovery codes, prints them once, creates the admin in a platform transaction, and audits bootstrap. Reject stdin redirection unless an explicit test-only reader is injected by unit tests.

- [ ] **Step 5: Implement local authentication**

Authentication order avoids account enumeration: normalize identifier, load candidate, verify password, enforce lock/source CIDR, verify TOTP or one recovery code, update failure/lock state, and append audit. Client-facing errors remain generic.

- [ ] **Step 6: Verify and commit**

```powershell
go test -race ./internal/platform/keyring ./internal/identity -v
go run ./cmd/control-plane admin bootstrap --help
powershell -NoProfile -File scripts/ci.ps1
git add migrations/00004_local_identity.sql sql/queries/local_identity.sql internal/platform/keyring internal/identity cmd/control-plane/admin.go
git commit -m "feat: add break-glass authentication"
```

### Task 6: OIDC, Keycloak Contract, Server Sessions, and CSRF

**Files:**

- Create: `migrations/00005_oidc_sessions.sql`
- Create: `sql/queries/oidc_sessions.sql`
- Create: `internal/identity/oidc.go`
- Create: `internal/identity/session.go`
- Create: `internal/identity/csrf.go`
- Create: `internal/identity/oidc_test.go`
- Create: `internal/identity/session_test.go`
- Create: `internal/identity/keycloak_integration_test.go`
- Extend: `internal/testkit/keycloak.go`

- [ ] **Step 1: Write failing session tests**

Cover opaque cookie token hashing, idle/absolute expiry, rotation, revocation, Secure/HttpOnly/SameSite flags, explicit development downgrade, CSRF token, Origin rejection, and membership/role invalidation.

- [ ] **Step 2: Add OIDC/session migration**

Create OIDC identity, auth transaction, and session tables with digests, state/nonce/verifier, expiry, revocation, and indexes. Browser session plaintext is never stored.

- [ ] **Step 3: Implement OIDC client abstraction**

```go
type OIDCProvider interface {
	AuthorizationURL(state, nonce, challenge, redirect string) string
	ExchangeAndVerify(context.Context, code, verifier string) (Claims, error)
}

type Claims struct {
	Issuer  string
	Subject string
	Email   string
	Name    string
}
```

Use Discovery, PKCE S256, state, nonce, issuer, audience, time checks, and bounded JWKS refresh for unknown `kid`.

- [ ] **Step 4: Implement JIT identity and sessions**

OIDC callback may create `user_identities` and `oidc_identities` but not membership/roles. Create a server session and cookie, audit both login and JIT creation, and return an authenticated session with zero tenant authorization when unassigned.

- [ ] **Step 5: Add Keycloak integration tests**

Start the locked Keycloak image with an imported test realm. Test Discovery, successful PKCE flow, state/nonce mismatch, invalid audience/issuer, JIT identity without membership, and JWKS rotation. Do not use browser automation; call authorization/token endpoints through a deterministic test client.

- [ ] **Step 6: Verify and commit**

```powershell
go test -race ./internal/identity -run 'Test(Session|OIDC|Keycloak)' -v
powershell -NoProfile -File scripts/ci.ps1
git add migrations/00005_oidc_sessions.sql sql/queries/oidc_sessions.sql internal/identity internal/testkit/keycloak.go
git commit -m "feat: add oidc and server sessions"
```

### Task 7: Service Identities and Opaque Tokens

**Files:**

- Create: `migrations/00006_service_identities.sql`
- Create: `sql/queries/service_identities.sql`
- Create: `internal/identity/service_identity.go`
- Create: `internal/identity/service_identity_test.go`
- Create: `internal/identity/service_identity_isolation_test.go`

- [ ] **Step 1: Write failing lifecycle tests**

Cover one-time plaintext return, prefix lookup, Argon2id verification, 24-hour default maximum, tenant/platform exclusivity, source CIDR, two-token rotation limit, revocation, expiry, audit, and cross-tenant isolation.

- [ ] **Step 2: Add schema and RLS**

Create service identity and service token tables. Enforce exactly one scope with a check constraint:

```sql
CHECK ((scope = 'platform' AND tenant_id IS NULL) OR
       (scope = 'tenant' AND tenant_id IS NOT NULL))
```

Tenant rows use forced RLS. Platform rows are accessed only through the platform pool.

- [ ] **Step 3: Implement token format**

Use format `ajs_<12-char-prefix>_<43-char-base64url-secret>`. Store only prefix and Argon2id verifier. Creation/rotation returns plaintext once; repository reads never expose it.

- [ ] **Step 4: Implement authentication and rotation**

Reject malformed prefixes before database work. Load bounded candidates by prefix/scope, verify in constant time, enforce expiry/revocation/CIDR, and audit. Rotation creates the new token before the caller revokes the old token, with at most two active tokens.

- [ ] **Step 5: Verify and commit**

```powershell
go test -race ./internal/identity -run 'TestService' -v
powershell -NoProfile -File scripts/ci.ps1
git add migrations/00006_service_identities.sql sql/queries/service_identities.sql internal/identity
git commit -m "feat: add service identity tokens"
```

### Task 8: Versioned API, OpenAPI, Idempotency, Pagination, and Module Routes

**Files:**

- Create: `migrations/00007_idempotency.sql`
- Create: `sql/queries/idempotency.sql`
- Create: `api/openapi/control-plane.yaml`
- Create: `internal/platform/httpserver/idempotency.go`
- Create: `internal/platform/httpserver/pagination.go`
- Create: `internal/platform/httpserver/openapi_test.go`
- Create: `internal/tenancy/http.go`
- Create: `internal/identity/http.go`
- Create: `internal/audit/http.go`
- Modify: `internal/platform/httpserver/router.go`

- [ ] **Step 1: Write failing API convention tests**

Cover error envelope, request ID, snake_case JSON, body size, cursor validation, default/max page size, PATCH version conflict, required Idempotency-Key, replay, body mismatch conflict, CSRF/Origin, and tenant URL escalation.

- [ ] **Step 2: Add idempotency schema**

Store actor, method, canonical route, key, SHA-256 body hash, response status/body, creation and expiry. RLS protects tenant records. Unique scope is actor/method/route/key. Never store request bodies or secrets.

- [ ] **Step 3: Implement stable errors and pagination**

```go
type ErrorEnvelope struct { Error APIError `json:"error"` }
type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

func EncodeCursor(createdAt time.Time, id uuid.UUID) string
func DecodeCursor(string) (time.Time, uuid.UUID, error)
```

Cursor is base64url of a versioned binary payload and is opaque to clients.

- [ ] **Step 4: Implement idempotent writes**

Canonicalize route templates, hash the exact validated JSON bytes, reserve the key inside the business transaction, and persist status/body before commit. Concurrent identical requests wait for or return the committed result; mismatched hashes return HTTP 409 `idempotency_conflict`.

- [ ] **Step 5: Write OpenAPI 3.1 and handlers**

Document every Phase 2 route from the approved spec. Define UUID, RFC3339 time, cursor, error, tenant, membership, role binding, service identity, token-created, session, and audit schemas. Handlers call module services and never issue SQL.

- [ ] **Step 6: Add OpenAPI conformance tests**

Load the document with kin-openapi, validate it, enumerate chi routes, and fail when a documented operation is missing or a Phase 2 route is undocumented. Exercise representative requests and validate request/response bodies and status codes.

- [ ] **Step 7: Verify and commit**

```powershell
go test -race ./internal/platform/httpserver ./internal/tenancy ./internal/identity ./internal/audit -v
powershell -NoProfile -File scripts/ci.ps1
git add migrations/00007_idempotency.sql sql/queries/idempotency.sql api/openapi internal
git commit -m "feat: expose phase 2 management api"
```

### Task 9: Control-Plane Image, Full Integration, Migration Rehearsal, and Exit Gate

**Files:**

- Create: `Dockerfile.control-plane`
- Create: `.github/workflows/control-plane-ci.yml`
- Create: `tests/integration/phase2_test.go`
- Create: `tests/isolation/phase2_isolation_test.go`
- Create: `tests/security/secret_log_test.go`
- Create: `docs/operations/control-plane-phase2.md`
- Modify: `scripts/control-plane-ci.ps1`
- Modify: `scripts/ci.ps1`

- [ ] **Step 1: Write failing phase-exit integration test**

The test starts locked PostgreSQL and Keycloak, applies migrations, boots the control plane, and proves:

- OIDC JIT user has no tenant membership.
- Platform admin creates two tenants.
- Tenant A admin cannot read/write Tenant B resources.
- Stale tenant update returns 409.
- Idempotent retry returns the stored response and body mismatch returns conflict.
- Service token creation returns plaintext once and later reads do not.
- Every security-sensitive operation has an audit event.
- PostgreSQL restart preserves session/idempotency state.

Expected RED: the complete assembled service/image/CI is absent.

- [ ] **Step 2: Add the control-plane image**

Use the digest-locked Go build image and digest-locked minimal runtime. Build `CGO_ENABLED=0` for amd64/arm64, run as numeric non-root, copy CA certificates and the binary root-owned mode 0555, use a read-only-compatible filesystem, and expose no embedded secrets.

- [ ] **Step 3: Add secret-log scanning**

Tests inject unique canary values for password, TOTP secret, recovery code, OIDC token, session token, and service token; exercise success and failure paths; then scan captured logs, traces, HTTP errors, audit details, and metric text. Every canary must be absent.

- [ ] **Step 4: Add migration compatibility rehearsal**

CI sequence:

1. Start an empty PostgreSQL database.
2. Start the current control-plane binary against the empty schema and assert `/readyz` returns 503 with the redacted schema-incompatible reason.
3. Apply every Phase 2 migration.
4. Assert the already-running control plane transitions to ready after its bounded readiness recheck.
5. Run current integration tests without restarting PostgreSQL.
6. Restart PostgreSQL and assert sessions and idempotency records remain usable.
7. In a separate database, migrate fully up, fully down, and fully up again.

- [ ] **Step 5: Add immutable CI**

Workflow jobs use pinned action SHAs and pinned QEMU/Buildx/BuildKit/Trivy images, matching Phase 1 supply-chain policy. Jobs include unit/race/static analysis, PostgreSQL migration/isolation, Keycloak OIDC, OpenAPI conformance, amd64/arm64 image build, per-architecture SBOM and vulnerability scan, and the existing Runner CI.

- [ ] **Step 6: Add operations documentation**

Document development startup, configuration and secret files, local-admin bootstrap, migrations, health behavior, OIDC/Keycloak test setup, backup expectations, and explicit Phase 2 exclusions. Do not call the service production-ready before later Compose/Helm phases.

- [ ] **Step 7: Run final verification**

```powershell
powershell -NoProfile -File scripts/ci.ps1
powershell -NoProfile -File scripts/control-plane-ci.ps1
go test -race ./...
go vet ./...
go tool staticcheck ./...
go tool sqlc generate
git diff --exit-code
docker buildx build --no-cache --platform linux/amd64,linux/arm64 --file Dockerfile.control-plane --output type=cacheonly .
git diff --check
git status --short
```

Expected: every command exits zero and the worktree is clean.

- [ ] **Step 8: Commit the integration gate**

```powershell
git add Dockerfile.control-plane .github/workflows/control-plane-ci.yml tests docs/operations/control-plane-phase2.md scripts
git commit -m "ci: complete phase 2 control-plane gate"
```

## Plan Self-Review Checklist

Before implementation begins, verify:

- Every approved Phase 2 design section maps to at least one task.
- No Task introduces Phase 3 account/secret-provider or data-plane behavior.
- All tenant SQL is covered by both application-scope and RLS tests.
- OIDC, local, and service authentication all audit success and failure.
- Migrations, generated sqlc code, OpenAPI, and image locks have clean-diff checks.
- Phase 1 CI remains a required gate throughout.
- All code-generating or dependency-resolving commands use explicit versions.
- Real external credentials are never required.
