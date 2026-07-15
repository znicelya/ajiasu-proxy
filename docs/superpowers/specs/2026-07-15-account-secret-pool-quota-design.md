# Phase 3 Account, Secret, Pool, and Quota Design

## 1. Goal

Phase 3 adds tenant-scoped AJiaSu account inventory, encrypted credential rotation, account pools, capacity accounting, quotas, and bulk import to the Phase 2 control plane. It freezes account references and the secret-provider contract for Phase 4.

It does not start AJiaSu, allocate nodes, create Runners, expose proxy endpoints, coordinate Redis leases, or provide any management API that returns stored plaintext credentials.

## 2. Confirmed decisions

- PostgreSQL remains authoritative and every new business table uses forced RLS.
- Tenant scope comes only from the authenticated route context.
- Tenant administrators mutate accounts, credentials, pools, and tenant quotas. Auditors may read safe metadata and capacity summaries. Operators may read account/pool status but cannot mutate credentials.
- Platform administrators do not gain an implicit tenant credential path.
- Every account has `max_concurrency`, default `1`, constrained to `1..32`.
- Credential material is modeled as a username and password payload, but the provider encrypts opaque bytes so later credential formats do not change database semantics.
- The default provider uses envelope encryption: a random 256-bit DEK encrypts each credential record with AES-256-GCM; the deployment keyring wraps that DEK with separate authenticated context.
- Vault and KMS adapters store an external reference or provider ciphertext through the same `secrets.Provider` interface. They depend on small client interfaces rather than vendor SDKs in the domain package.
- Credential history is append-only. Rotation creates a new active version and retires the previous version in one transaction.
- HTTP responses expose credential version/provider metadata only. There is no credential read/export endpoint.
- Account deletion is logical in Phase 3. Active capacity reservations or pool memberships prevent final deletion.
- Pool membership is explicit. A pool may additionally define a label selector that every explicit member must match.
- Phase 3 capacity is `sum(max_concurrency - active_reservations)` across eligible members. A database reservation primitive is included for Phase 4; no scheduler is implemented.
- Bulk import accepts at most 100 items, validates every item before execution, uses one tenant transaction with per-item savepoints, and returns an ordered per-item result without echoing credentials.

## 3. Secret-provider contract

```go
type Context struct {
    TenantID uuid.UUID
    AccountID uuid.UUID
    Version int64
    Purpose string
}

type SealedSecret struct {
    Provider string
    KeyID string
    Ciphertext []byte
    WrappedDEK []byte
    ExternalRef string
}

type Provider interface {
    Seal(context.Context, Context, []byte) (SealedSecret, error)
    Open(context.Context, Context, SealedSecret) ([]byte, error)
    Destroy(context.Context, Context, SealedSecret) error
}
```

Rules:

- `Context` is validated and serialized canonically as authenticated data.
- `Seal` never retains the caller's plaintext buffer.
- `Open` is an internal trusted operation for future Runner credential delivery. HTTP handlers and account read services do not expose it.
- Provider errors are mapped to stable storage/dependency errors and never include ciphertext, references, key IDs supplied by users, or plaintext.
- The envelope provider stores both ciphertext and wrapped DEK. Vault/KMS adapters store only fields required by their backend contract.
- Ciphertext copied between tenant/account/version contexts must fail authentication.

## 4. Data model

### 4.1 Tenant quotas

`tenancy.tenant_quotas` has exactly one row per tenant:

- `max_accounts`, default `100`, range `1..1000`.
- `max_pools`, default `50`, range `1..500`.
- `max_pool_memberships`, default `1000`, range `1..10000`.
- `version`, `created_at`, and `updated_at`.

Tenant creation inserts the quota row in the same transaction. Phase 3 migration backfills existing tenants.

Quota checks take the existing tenant advisory lock, count current rows, and create resources before the lock is released. Concurrent requests cannot exceed a limit.

### 4.2 Accounts

`accounts.accounts`:

- UUIDv7 ID and tenant ID.
- Unique tenant-scoped normalized name.
- Lifecycle: `active`, `disabled`, `deleting`.
- Health: `unknown`, `healthy`, `degraded`, `unhealthy`, `quarantined`.
- Optional service membership identifier and membership expiry.
- String label map stored as validated JSON.
- `max_concurrency` and monotonically increasing `version`.
- Health transition counters and last health timestamp for future Agent reports.

`accounts.account_credentials`:

- Account/tenant/version identity.
- Provider, key ID, ciphertext, wrapped DEK, or external reference.
- `created_at`, `retired_at`, and creator actor ID.
- Exactly one active credential version per account.

`accounts.account_capacity_reservations`:

- Tenant, account, owner resource ID, creation and expiry.
- Unique owner per account.
- Reservation creation locks the account and rejects when active reservations equal `max_concurrency`.
- Expired reservations do not consume capacity and may be purged safely.

### 4.3 Pools

`pools.account_pools`:

- Tenant-scoped unique name.
- Strategy: `least_connections`, `round_robin`, or `fixed_priority`.
- Required label selector as a validated JSON object; `{}` matches all explicit members.
- Lifecycle: `active`, `disabled`, `deleting`.
- Version and timestamps.

`pools.account_pool_memberships`:

- Pool/account/tenant identity.
- Priority `0..1000`, weight `1..100`, enabled flag, optional expiry.
- Unique account per pool.
- A membership is eligible only when the account and pool are active, health is not unhealthy/quarantined, membership has not expired, and account labels match the pool selector.

Capacity summaries contain total members, eligible members, total concurrency, reserved concurrency, and available concurrency. They contain no credential or external membership values.

## 5. State transitions

Accounts:

- `active -> disabled|deleting`
- `disabled -> active|deleting`
- `deleting` is terminal in Phase 3.

Pools use the same transition shape.

Credential rotation is allowed only for non-deleting accounts. Disabling an account blocks new reservations but does not delete credential history.

Health updates are internal service operations. Consecutive thresholds are stored, but Agent-driven debounce and quarantine automation remain Phase 4/6 work.

## 6. API

All routes are under `/api/v1/tenants/{tenant_id}` and use Phase 2 authentication, CSRF, idempotency, cursor, error, and version conventions.

```text
GET    /accounts
POST   /accounts
POST   /accounts/bulk-import
GET    /accounts/{account_id}
PATCH  /accounts/{account_id}
POST   /accounts/{account_id}/credentials/rotate

GET    /account-pools
POST   /account-pools
GET    /account-pools/{pool_id}
PATCH  /account-pools/{pool_id}
GET    /account-pools/{pool_id}/members
POST   /account-pools/{pool_id}/members
DELETE /account-pools/{pool_id}/members/{membership_id}
GET    /account-pools/{pool_id}/capacity

GET    /quota
PATCH  /quota
```

Create and rotate requests accept credential material, but responses return only safe account and credential-version metadata. Idempotency storage for credential-bearing requests hashes the exact request bytes and encrypts the safe response when configured; it never stores the request body.

Bulk results contain index, optional created account ID, result code, and stable message. They never include the submitted username, password, or arbitrary labels.

## 7. Authorization

- `tenant_admin`: all Phase 3 read/write operations in the granted tenant.
- `operator`: read accounts, pools, membership, quota, and capacity.
- `auditor`: read the same safe metadata.
- `consumer`: denied.
- `platform_admin`: denied unless it also has an explicit tenant grant. Platform status operations do not bypass tenant authorization.

Every route tenant ID must be present in the authenticated principal grants. Resource IDs cannot expand scope.

## 8. Audit and outbox

Security-sensitive actions append audit and Outbox records in the business transaction:

- Account created, updated, disabled, and marked deleting.
- Credential created/rotated and provider destroy request.
- Pool created/updated and membership added/removed.
- Quota updated and quota rejection.
- Bulk import completed with safe aggregate counts.
- Capacity reserved/released and capacity rejection.

Details are whitelisted identifiers, versions, counts, lifecycle/health values, provider category, and result codes. Usernames, passwords, ciphertext, wrapped keys, external references, selectors supplied as raw JSON, and labels supplied as raw JSON are forbidden.

## 9. Failure behavior

- Secret-provider failure rolls back account creation or rotation.
- Audit or Outbox failure rolls back the business change.
- Missing tenant transaction context never falls back to an unscoped query.
- Stale versions return `resource_version_conflict`.
- Quota conflicts return `quota_exceeded` with the quota category only.
- Capacity conflicts return `account_capacity_exhausted`.
- Vault/KMS dependency loss returns `dependency_unavailable` without backend text.

## 10. Testing and exit criteria

Phase 3 is complete when:

1. Envelope ciphertext cannot be opened with a different master key or authenticated context.
2. Vault/KMS adapter contract tests prove references are tenant/account/version bound.
3. Account, credential, quota, pool, membership, reservation, and audit tables pass forced-RLS cross-tenant tests.
4. Concurrent account/pool creation cannot exceed tenant quotas.
5. Concurrent reservations cannot exceed account concurrency.
6. Credential creation/rotation never returns stored plaintext through GET/list routes.
7. Bulk import returns deterministic per-item results and never logs or audits submitted credentials.
8. Pool capacity summaries match eligible members and active reservations transactionally.
9. Migration up/down/up, PostgreSQL restart, sqlc, race, vet, Staticcheck, OpenAPI, and multiarch image gates pass.
10. No Phase 4 scheduling, node, Runner, Redis, or endpoint behavior is introduced.
