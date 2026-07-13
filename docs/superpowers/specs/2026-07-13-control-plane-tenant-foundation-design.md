# Control-Plane and Tenant Foundation Design

**Date:** 2026-07-13  
**Status:** Approved conversational design, pending written-spec review  
**Phase:** 2 of the AJiaSu Enterprise Proxy Platform roadmap

## 1. Goal

Build the first Go control-plane increment: a secure, multi-tenant management API foundation with PostgreSQL-backed identity, tenancy, RBAC, audit, sessions, migrations, idempotency, and API conventions.

Phase 2 must leave stable interfaces for Phase 3 account and secret management. It does not implement AJiaSu account inventory, account pools, proxy endpoints, scheduling, node agents, proxy gateways, Redis coordination, or the web console.

## 2. Confirmed Technology Decisions

- Go modular monolith.
- `net/http` with `chi` routing.
- `pgx` for PostgreSQL access.
- `sqlc` for typed SQL generation.
- Goose for reviewed SQL migrations with up/down paths.
- OpenAPI 3.1 for the external management contract.
- PostgreSQL as the authoritative store for sessions, idempotency, audit, and Phase 2 background state.
- Standard OIDC Discovery and Authorization Code with PKCE, tested against Keycloak.
- Server-side browser sessions plus separate opaque service tokens.
- Application-enforced tenant scope plus PostgreSQL Row-Level Security.
- AES-256-GCM Keyring backed by a deployment-supplied 32-byte key file for local-admin TOTP secrets.

## 3. Scope

### 3.1 Included

- Control-plane process bootstrap and graceful shutdown.
- Immutable validated configuration.
- JSON logging, request IDs, health endpoints, base metrics, and tracing hooks.
- PostgreSQL connection pools, transaction helper, migrations, sqlc generation, and test database tooling.
- Tenant lifecycle and tenant-scoped membership.
- Fixed platform and tenant roles.
- OIDC identities and login.
- Local break-glass administrator bootstrap and login with password plus TOTP.
- Server-side browser sessions and CSRF protection.
- Tenant-scoped and platform-scoped service identities using one-time opaque tokens.
- Append-only audit events and transaction Outbox foundation.
- Versioned `/api/v1` conventions, pagination, stable errors, optimistic concurrency, and idempotent writes.
- Cross-tenant isolation, migration, authentication, audit, and OpenAPI contract tests.

### 3.2 Excluded

- AJiaSu accounts, encrypted account credentials, account pools, and quotas.
- Proxy endpoints and proxy credentials.
- Node registration, Agent RPC, Runner reconciliation, and scheduling.
- HTTP, HTTPS CONNECT, or SOCKS5 data plane.
- Redis.
- Custom roles or policy expression languages.
- OIDC group-to-role or group-to-tenant mapping.
- Browser UI.
- Vault, cloud KMS, Kubernetes workload identity, and Agent mTLS.
- Public SaaS registration, billing, and customer self-service.

## 4. Architecture and Package Boundaries

```text
HTTP client
    |
    v
chi router and middleware
    |
    +-- identity module
    +-- tenancy module
    +-- audit module
    |
    v
pgx + sqlc + explicit transaction boundary
    |
    v
PostgreSQL + RLS
```

Target structure:

```text
api/
└── openapi/
cmd/
└── control-plane/
internal/
├── platform/
│   ├── config/
│   ├── database/
│   ├── httpserver/
│   ├── logging/
│   ├── requestctx/
│   └── keyring/
├── identity/
├── tenancy/
├── audit/
└── testkit/
migrations/
sql/
├── queries/
└── schema/
```

Rules:

- `cmd/control-plane` only composes dependencies, starts servers, and coordinates graceful shutdown.
- `internal/platform` cannot depend on a business module.
- Identity, tenancy, and audit own their tables and expose explicit service interfaces for cross-module work.
- A module cannot bypass another module by issuing SQL against its tables.
- HTTP DTOs, domain types, and sqlc-generated types are separate.
- Repository methods accept an explicit transaction or database executor.
- Business workflows cannot open hidden nested transactions.
- OpenAPI and sqlc are adapters and generators, not domain models.

## 5. Data Model

Core tables:

```text
tenants
user_identities
oidc_identities
local_admins
auth_sessions
memberships
role_bindings
service_identities
service_tokens
idempotency_records
audit_events
outbox_events
```

### 5.1 Tenants and lifecycle

- IDs use UUIDv7.
- Timestamps use UTC `timestamptz`.
- Tenant states are `active`, `suspended`, and `deleting`.
- Phase 2 does not physically delete a tenant.
- Suspended tenants reject new authenticated tenant operations while retaining auditable state.
- Mutable resources carry a monotonically increasing `version`.

### 5.2 User identities

- `user_identities` is a global principal table.
- An OIDC identity is uniquely bound by immutable `issuer + subject`.
- Email, display name, and claims are profile metadata and never identity keys.
- First OIDC login may create identity records but grants no membership or role.
- Disabled identities cannot create or refresh sessions.

### 5.3 Membership and roles

- A membership connects one global identity to one tenant.
- Removing a membership revokes its role bindings and tenant session authorization.
- Fixed roles are `platform_admin`, `tenant_admin`, `operator`, `auditor`, and `consumer`.
- `platform_admin` is platform-scoped and cannot be represented as a tenant binding.
- Tenant roles always include `tenant_id`.
- Phase 2 does not support custom roles.

### 5.4 Service identities

- A service identity is platform-scoped or tenant-scoped, never both.
- Tokens are cryptographically random opaque values with a non-secret lookup prefix.
- Plaintext is returned once.
- PostgreSQL stores an Argon2id verifier, prefix, creation time, expiry, and revocation time.
- Default maximum validity is 24 hours.
- At most two active tokens per service identity support rotation.
- Token constraints may include role, tenant, source CIDR, and expiry.

### 5.5 Sessions and authentication transactions

- Browser session plaintext tokens exist only in cookies and request memory.
- PostgreSQL stores token digests, identity, issued time, last-used time, idle deadline, absolute deadline, and revocation data.
- OIDC authentication transactions store state, nonce, PKCE verifier, return path, and a short expiry.
- Session and authentication transaction tokens are high-entropy random values.

## 6. PostgreSQL Roles, Transactions, and RLS

### 6.1 Application role

- All tenant tables enable and force RLS.
- The normal application database role does not have `BYPASSRLS`.
- Tenant transactions begin by setting transaction-local context:

```sql
SET LOCAL app.tenant_id = '<tenant UUID>';
SET LOCAL app.actor_id = '<actor UUID>';
```

- Policies compare rows to `current_setting('app.tenant_id', true)`.
- Tenant scope comes from authenticated server context, not an arbitrary URL or JSON value.
- Only `SET LOCAL` is allowed so pooled connections cannot retain tenant state after commit or rollback.

### 6.2 Platform maintenance role

- Cross-tenant platform work uses a separate PostgreSQL role and separate pgx pool.
- Platform operations require explicit service methods and audit metadata.
- Request handlers cannot silently switch from the tenant pool to the platform pool.
- Platform administrators reading cross-tenant audit data generate a secondary audit event.

### 6.3 Transaction contract

- Business state, audit record, idempotency result, and Outbox event commit atomically when applicable.
- Audit failure rolls back the security-sensitive business operation.
- sqlc queries execute through the supplied transaction executor.
- Transaction retries are limited to explicitly classified serialization/deadlock errors and must preserve idempotency.

### 6.4 Audit immutability

- Application roles cannot update or delete `audit_events`.
- Database triggers reject update/delete attempts as defense in depth.
- Audit details are constructed from whitelisted fields rather than serialized request bodies.

## 7. Identity and Authentication

### 7.1 OIDC

Flow:

1. `GET /api/v1/auth/oidc/login` creates a short-lived authentication transaction.
2. The server generates state, nonce, and a PKCE verifier.
3. The browser receives only the authorization redirect.
4. The callback verifies state, authorization code, PKCE, nonce, issuer, audience, signature, and time claims.
5. The server finds or creates the identity by `issuer + subject`.
6. The server creates a database session and sets an opaque cookie.
7. Login, failure category, and JIT identity creation are audited.

Compatibility:

- The implementation uses standards-based Discovery and JWKS.
- Keycloak is the integration-test provider.
- Unknown `kid` values trigger bounded JWKS refresh to support rotation.
- Discovery/JWKS failure blocks new OIDC logins without invalidating already valid local sessions.
- Group claims do not grant membership or roles in Phase 2.

### 7.2 Browser sessions

- Cookie flags are `Secure`, `HttpOnly`, and `SameSite=Lax` with a restricted path.
- Secure-cookie disabling requires explicit development mode and is rejected in production mode.
- Idle timeout defaults to 30 minutes.
- Absolute lifetime defaults to 12 hours.
- Login, privilege elevation, and sensitive identity operations rotate the session token.
- Identity disablement, membership removal, and role revocation invalidate relevant authorization.
- Unsafe methods require a synchronizer CSRF token and a trusted `Origin`.

### 7.3 Local break-glass administrator

- Creation uses an interactive `control-plane admin bootstrap` command.
- Secrets cannot be passed as command-line arguments or long-lived environment variables.
- Passwords use Argon2id.
- TOTP is mandatory.
- TOTP secrets are encrypted with AES-256-GCM through a `Keyring` interface.
- The initial Keyring reads a deployment-supplied, permission-restricted 32-byte file.
- Recovery codes are generated once and stored as one-way verifiers.
- Local login is disabled by default and requires configured source CIDRs.
- Password, TOTP, and recovery failures are rate limited by account and source address.
- Repeated failures produce a bounded temporary lockout.
- Local administrators are platform administrators and cannot impersonate tenant users.

### 7.4 Service tokens

- Service token plaintext is shown only at creation or rotation.
- Lookup uses a non-secret prefix followed by Argon2id verification.
- Authentication, failure, rotation, expiry, and revocation are audited.
- Full token values never enter logs, traces, metrics, errors, or audit details.
- Kubernetes workload identity and Agent mTLS remain Phase 4 responsibilities.

## 8. Keyring

- The initial interface supports encrypt and decrypt operations with authenticated context.
- AES-256-GCM uses an independent random nonce for every encryption.
- Authenticated context includes the local-admin ID and secret purpose.
- The key file must be a regular file, have restricted permissions, and contain exactly 32 bytes.
- Key material is never stored in PostgreSQL or logged.
- Missing, malformed, or inaccessible key material prevents startup.
- Phase 3 may add Vault/KMS implementations without changing the local-admin API.

## 9. HTTP API

### 9.1 Routes

```text
GET    /livez
GET    /readyz

GET    /api/v1/auth/session
POST   /api/v1/auth/logout
GET    /api/v1/auth/oidc/login
GET    /api/v1/auth/oidc/callback
POST   /api/v1/auth/local/login

GET    /api/v1/tenants
POST   /api/v1/tenants
GET    /api/v1/tenants/{tenant_id}
PATCH  /api/v1/tenants/{tenant_id}

GET    /api/v1/tenants/{tenant_id}/members
POST   /api/v1/tenants/{tenant_id}/members
DELETE /api/v1/tenants/{tenant_id}/members/{membership_id}

GET    /api/v1/tenants/{tenant_id}/role-bindings
POST   /api/v1/tenants/{tenant_id}/role-bindings
DELETE /api/v1/tenants/{tenant_id}/role-bindings/{binding_id}

GET    /api/v1/service-identities
POST   /api/v1/service-identities
POST   /api/v1/service-identities/{id}/tokens
DELETE /api/v1/service-identities/{id}/tokens/{token_id}

GET    /api/v1/audit-events
```

### 9.2 Conventions

- JSON fields use `snake_case`.
- Times use RFC 3339 UTC.
- Responses include `X-Request-ID`.
- Request bodies default to a 1 MiB maximum.
- Server read, header, write, idle, and shutdown timeouts are explicit.
- Collections use opaque cursor pagination with default 50 and maximum 200.
- UUIDs and cursors are validated before entering services.

Stable error envelope:

```json
{
  "error": {
    "code": "resource_version_conflict",
    "message": "resource version does not match",
    "request_id": "01900000-0000-7000-8000-000000000000",
    "details": {}
  }
}
```

- Clients cannot depend on PostgreSQL, OIDC, cryptography, or Go internal error text.
- Panic recovery returns a generic internal error and logs a request ID plus stack internally.
- Dependency loss maps to a stable `dependency_unavailable` response where the request can be handled safely.

### 9.3 Optimistic concurrency

- PATCH requires the current resource version.
- SQL updates include `WHERE id = $id AND version = $expected_version`.
- Zero updated rows caused by a stale version return HTTP 409 and `resource_version_conflict`.

### 9.4 Idempotency

- Mutating API calls require `Idempotency-Key`.
- Scope is actor, HTTP method, canonical route, key, and request-body hash.
- A matching retry returns the stored status and body.
- The same key with a different body returns `idempotency_conflict`.
- The idempotency record and business result commit atomically.
- Records have a bounded retention period and do not contain plaintext secrets.

## 10. RBAC

- Authorization is handled by a shared Policy Service, not individual handlers.
- Default is deny.
- Unknown actors, roles, actions, or tenant scope are denied.
- `platform_admin` manages tenants, platform service identities, and break-glass administration.
- `tenant_admin` manages members and role bindings in its tenant.
- `operator`, `auditor`, and `consumer` exist as stable role foundations but do not gain access to resources not implemented in Phase 2.
- An URL `tenant_id` cannot expand the authenticated tenant set.
- Role changes and their audit records commit in one transaction.

## 11. Audit

Audit events contain:

- Actor type and actor ID when known.
- Tenant ID when applicable.
- Action and resource type/ID.
- Result category.
- Source IP and User-Agent.
- Request ID.
- UTC timestamp.
- Whitelisted, redacted details.

Rules:

- Authentication failures may omit actor ID but include a safe reason category.
- Passwords, TOTP data, recovery codes, OIDC tokens, session tokens, and service tokens are forbidden.
- `auditor` sees only its tenant events through RLS.
- Default list responses omit high-risk detail fields.
- Cross-tenant platform audit reads use the platform pool and are themselves audited.

## 12. Outbox Foundation

- Phase 2 writes Outbox events in the same transaction as business state.
- Events contain a stable type, aggregate reference, payload version, creation time, availability time, and processing lease fields.
- Payloads cannot contain plaintext credentials.
- Phase 2 provides database-backed polling and lease primitives only.
- Kafka, NATS, and external message delivery are excluded.

## 13. Configuration

Required categories:

- HTTP bind address and server timeouts.
- Normal and platform PostgreSQL DSNs and pool limits.
- Migration mode/version policy.
- OIDC issuer, client ID, client secret file, and redirect URL.
- Cookie name, secure mode, domain/path, idle timeout, and absolute timeout.
- Keyring file.
- Local-login enabled flag and allowed CIDRs.
- Logging, metrics, and tracing settings.

Rules:

- Configuration is parsed once into an immutable structure.
- Missing, contradictory, or weak production security settings prevent startup.
- Secret values use read-only files where practical.
- Logs may report that a feature is enabled but cannot print secret values or password-bearing DSNs.
- Runtime hot reload is excluded.

## 14. Observability and Health

- JSON logs use stable fields: timestamp, level, component, request ID, actor ID, tenant ID, and error code.
- `/livez` reports process liveness only.
- `/readyz` checks PostgreSQL, migration compatibility, and critical configuration state.
- Health responses do not reveal DSNs, schema details, OIDC documents, or internal errors.
- Metrics cover request latency/count, authentication outcomes, active sessions, database pool state, RLS denial, idempotency conflicts, and audit failures.
- Tenant IDs, user IDs, token prefixes, and path parameters are not unbounded metric labels.
- OpenTelemetry hooks cover HTTP, database, and authentication flows without recording secrets.

## 15. Failure Behavior

- PostgreSQL loss makes readiness fail and produces stable dependency errors for affected requests.
- Audit-write failure rolls back security-sensitive operations.
- OIDC metadata/JWKS loss blocks new OIDC login but not otherwise valid sessions.
- Keyring failure prevents startup.
- Missing RLS context is treated as an internal security fault and alert, never an unscoped query mode.
- Outer middleware recovers panics, emits a generic response, and logs the stack internally with the request ID.

## 16. Migrations

- Goose executes reviewed SQL migrations.
- Every migration has up and down behavior.
- CI exercises up, down, and up again against a real PostgreSQL instance.
- Migrations include grants, RLS policies, triggers, indexes, constraints, and database roles where applicable.
- Schema changes follow expand-migrate-contract when rolling compatibility is required.
- The control plane refuses readiness when the schema is outside its supported migration range.

## 17. Testing

### 17.1 Unit tests

- Configuration parsing and secret redaction.
- Error mapping and request IDs.
- Cursor encoding/decoding.
- Idempotency body hashing and conflict behavior.
- RBAC decisions.
- Password, TOTP, recovery code, session, and service-token logic.
- Tenant and membership state transitions.

### 17.2 PostgreSQL integration tests

- Migration up/down/up.
- Transaction rollback and audit atomicity.
- RLS read/write/delete isolation.
- Missing RLS context.
- pgx connection reuse after commit and rollback.
- Audit update/delete rejection.
- Optimistic concurrency.
- Idempotency replay and conflict.
- Outbox lease behavior.

### 17.3 OIDC and HTTP tests

- Keycloak Discovery and Authorization Code with PKCE.
- State and nonce validation.
- Issuer and audience rejection.
- JIT identity creation without membership.
- Unknown-key refresh and JWKS rotation.
- OpenAPI request/response and status-code conformance.
- Pagination and stable error envelopes.
- CSRF and Origin enforcement.

### 17.4 Security tests

- Tenant A cannot access Tenant B memberships, role bindings, service identities, token metadata, idempotency records, or audit events.
- Logs and telemetry are scanned for passwords, TOTP secrets, recovery codes, OIDC tokens, session tokens, and service tokens.
- Local-login CIDR restrictions and rate limits are verified.
- Disabled identities, removed memberships, revoked roles, and revoked tokens lose access.

### 17.5 CI gates

- Phase 1 Runner gates remain green.
- Go formatting, tests, `go vet`, static analysis, and race tests.
- sqlc generation and clean-diff verification.
- Goose migration tests.
- OpenAPI validation and contract tests.
- Real PostgreSQL and Keycloak integration suites.
- Control-plane image SBOM and vulnerability scan.

## 18. Exit Criteria

Phase 2 is complete only when:

1. Management routes and OpenAPI schemas agree.
2. OIDC, break-glass, and service-identity authentication produce audit events.
3. Stale writes consistently return HTTP 409 with `resource_version_conflict`.
4. Every tenant resource passes application-scope and RLS isolation tests.
5. Migrations pass up/down/up and rolling-compatibility rehearsal.
6. PostgreSQL restart preserves sessions, state, and idempotency results.
7. No automated test finds a plaintext secret in logs or telemetry.
8. Keycloak tests cover PKCE, state, nonce, JIT identity, invalid claims, and key rotation.
9. Phase 1 Runner build, protocol, supply-chain, and image-security gates remain green.
10. No Phase 3 or data-plane behavior has been pulled into the phase.

## 19. Implementation Decomposition

The executable plan should use these independently reviewable increments:

1. Go workspace, toolchain locks, service bootstrap, configuration, logging, health, and CI.
2. PostgreSQL test environment, Goose migrations, pgx pools, sqlc, transaction helper, and RLS context.
3. Audit and Outbox foundations.
4. Tenant, membership, fixed-role RBAC, optimistic concurrency, and isolation tests.
5. Keyring, local-admin bootstrap, Argon2id, TOTP, recovery codes, and local login.
6. OIDC authentication transactions, Keycloak integration, and server-side sessions.
7. Service identities and opaque token lifecycle.
8. API conventions, idempotency, cursor pagination, OpenAPI, and HTTP contract tests.
9. Phase integration, migration rehearsal, secret-log scans, control-plane image, and exit verification.

Each increment must be test-driven, committed separately, and receive specification and code-quality review before the next increment starts.
