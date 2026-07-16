# AJiaSu Enterprise Proxy Platform Roadmap

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the approved enterprise multi-tenant proxy platform as nine independently testable increments.

**Architecture:** A Go modular-monolith control plane manages desired state in PostgreSQL, while Rust Gateway and Agent processes operate isolated AJiaSu Runners. Docker Compose and Kubernetes use the same APIs, images, configuration semantics, and reconciliation model.

**Tech Stack:** Go, Rust, TypeScript/React, PostgreSQL, Redis, OpenAPI 3.1, gRPC, Docker BuildKit, Docker Compose, Kubernetes, Helm, Prometheus, OpenTelemetry

---

## Planning Rule

This roadmap is the dependency and acceptance contract for the program. Each phase receives a separate executable TDD plan after the preceding phase stabilizes its public interfaces. Do not combine phases into one implementation branch, and do not start a later phase while an earlier exit gate is failing.

## Target Repository Layout

```text
.
├── api/
│   ├── openapi/
│   └── proto/
├── cmd/
│   └── control-plane/
├── internal/
│   ├── identity/
│   ├── tenancy/
│   ├── accounts/
│   ├── pools/
│   ├── endpoints/
│   ├── scheduler/
│   ├── nodes/
│   ├── reconciler/
│   ├── audit/
│   └── observability/
├── crates/
│   ├── agent/
│   ├── gateway/
│   ├── proxy-protocol/
│   └── policy/
├── web/
├── runner/
├── deploy/
│   ├── compose/
│   └── helm/ajiasu-platform/
├── migrations/
├── tests/
│   ├── contract/
│   ├── integration/
│   ├── isolation/
│   ├── e2e/
│   └── load/
├── docs/
│   ├── adr/
│   ├── operations/
│   └── superpowers/
└── scripts/
```

Files are grouped by responsibility. Go packages remain internal until a cross-process contract is necessary. Rust protocol parsing and policy evaluation live in reusable crates rather than inside Gateway handlers.

## Phase 1: Secure Runner and Repository Foundation

**Detailed plan:** `docs/superpowers/plans/2026-07-11-secure-runner-foundation.md`

**Produces:**

- Imported and documented repository baseline.
- Checksum-verified AJiaSu 4.2.3.0 artifacts for `linux/amd64` and `linux/arm64`.
- Minimal Runner image with an explicit capability contract.
- Fake-AJiaSu contract harness plus protected real-binary checks.
- CI checks for build, smoke tests, SBOM, vulnerabilities, and secret leakage.

**Exit gate:** No unchecked download is executed; both target architectures build; the fake contract suite passes; the real binary version/smoke check can run with protected credentials; critical image findings block release.

## Phase 2: Control-Plane and Tenant Foundation

**Entry dependency:** Phase 1 image and CI contracts are stable.

**Produces:**

- Go service bootstrap, configuration, structured logging, health endpoints, migrations, and transaction helper.
- Tenant, user identity, membership, role binding, service identity, and append-only audit schemas.
- OIDC Authorization Code with PKCE and local break-glass authentication with TOTP.
- `/api/v1` conventions, OpenAPI generation, request IDs, idempotency keys, pagination, stable errors, and optimistic concurrency.
- Server-derived tenant scope enforced in repositories and integration tests.

**Exit gate:** Cross-tenant isolation tests pass for all introduced resources; OIDC and break-glass paths are audited; stale writes return a stable conflict error; database changes survive restart and migration rollback rehearsal.

## Phase 3: Account, Secret, Pool, and Quota Management

**Entry dependency:** Phase 2 identity, tenancy, API, transaction, and audit interfaces are frozen for one minor version.

**Produces:**

- Secret-provider interface with envelope-encryption and Vault/KMS adapters.
- AJiaSu account lifecycle, credential rotation, membership metadata, health state, and configurable concurrency limit defaulting to one.
- Account pools, pool membership, selectors, priority, and capacity summaries.
- Tenant quotas and validated bulk account import with per-item results.
- Redaction tests proving plaintext credentials cannot reach logs, traces, errors, metrics, or audit detail.

**Exit gate:** Ciphertext cannot be decrypted without the configured key provider; proxy/API users cannot retrieve stored plaintext; pool capacity and quota invariants are transactionally enforced.

## Phase 4: Node Agent, Runner Lifecycle, and Reconciliation

**Entry dependency:** Phase 3 account references and secret-provider contract are stable.

**Produces:**

- Versioned gRPC Agent protocol supporting current and previous versions.
- Node registration, short-lived service identity, heartbeat, labels, capacity, and maintenance state.
- Idempotent Runner create/stop/rebuild/garbage-collect operations.
- Desired/observed resource state, operation IDs, finalizers, and reconciler retry policy.
- Isolated network namespace and restricted credential injection.

**Exit gate:** Duplicate commands produce one Runner; Agent restart reconstructs state; deleting an endpoint drains and cleans its Runner; one tenant cannot inspect another tenant's Runner files or network namespace.

## Phase 5: Rust Proxy Gateway and Access Policy

**Detailed spec:** `docs/superpowers/specs/2026-07-15-rust-proxy-gateway-access-policy-design.md`

**Detailed plan:** `docs/superpowers/plans/2026-07-15-rust-proxy-gateway-access-policy.md`

**Entry dependency:** Phase 4 exposes stable Runner routing and health information.

**Produces:**

- HTTP forward proxy, HTTPS CONNECT, and SOCKS5 TCP CONNECT.
- Endpoint-specific proxy credentials with one-time plaintext return and slow verifier storage.
- Source CIDR, destination CIDR/domain, port, connection, rate, idle-timeout, and traffic-quota enforcement.
- Explicit DNS resolution modes and platform safety denies for loopback, link-local, management, and metadata networks.
- Bounded parsing and resource use under malformed or slow clients.

**Exit gate:** Protocol compatibility suites pass; TLS is tunneled without interception; default SSRF protections cannot be weakened by a tenant; memory and task counts remain bounded under adversarial inputs.

## Phase 6: Scheduler, Leases, Health, and Failover

**Detailed spec:** `docs/superpowers/specs/2026-07-16-scheduler-leases-health-failover-design.md`

**Detailed plan:** `docs/superpowers/plans/2026-07-16-scheduler-leases-health-failover.md`

**Operations:** `docs/operations/control-plane-phase6.md`

**Compatibility:** `docs/operations/compatibility-matrix.md`

**Entry dependency:** Phases 3-5 expose stable account, node, Runner, Gateway, and policy contracts.

**Produces:**

- Fixed and pooled endpoint allocation.
- Least-connections, round-robin, and fixed-priority policies.
- Redis leases with fencing tokens and PostgreSQL committed assignment state.
- Process, tunnel, egress, and account health with debounce thresholds.
- Bounded rebuild, node migration, account quarantine, cooldown, and exponential backoff.
- Redis degraded mode that blocks unsafe new pool allocations without dropping existing safe traffic.

**Exit gate:** Concurrency limits cannot be oversold under competing control-plane replicas; duplicate/out-of-order events converge; failure injection produces an audited deterministic outcome.

## Phase 7: Docker Compose Production Package

**Entry dependency:** Phase 6 provides an end-to-end control/data path.

**Produces:**

- Development and production Compose profiles using the same images and configuration names as Kubernetes.
- PostgreSQL, Redis, Control Plane, Console, Gateway, Agent, Runner support, migrations, health checks, and persistent volumes.
- Generated production secrets with no image defaults.
- Backup, restore, upgrade, rollback, and graceful-shutdown scripts.
- End-to-end smoke tests for fixed and pooled endpoints across all supported proxy protocols.

**Exit gate:** A clean host can start, use, upgrade, back up, restore, and stop the platform from documented commands; Docker Socket access is restricted to the Agent boundary.

## Phase 8: Helm and Kubernetes Production Package

**Entry dependency:** Compose semantics and end-to-end behavior are stable.

**Produces:**

- Helm chart for Control Plane, Console, Gateway, Agent DaemonSet, Runner Pods, migrations, and configuration.
- External PostgreSQL/Redis production configuration and optional development dependencies.
- Pod security contexts, NetworkPolicy, RBAC, ServiceAccounts, disruption budgets, anti-affinity, topology spread, and graceful rollout.
- External Secrets/Vault/KMS integration points.
- Installation, rolling upgrade, node drain, Agent loss, Redis loss, and rollback tests in a temporary cluster.

**Exit gate:** Current and previous protocol versions roll together without downtime to the control plane; a node drain reschedules eligible Runners without quota or concurrency violation; security policy tests pass.

## Phase 9: Console, Operations, Performance, and Release Hardening

**Entry dependency:** All management APIs and deployment contracts are stable.

**Produces:**

- React console for tenants, members, accounts, pools, endpoints, operations, nodes, health, quotas, and audit.
- Prometheus alerts, Grafana dashboards, OpenTelemetry export, and SIEM audit export.
- PostgreSQL PITR guidance, encryption-key backup, restore exercise, and documented RPO/RTO evidence.
- 10,000-connection load suite, capacity report, resource defaults, and scaling guidance.
- Signed images, provenance, SBOM publication, release notes, compatibility matrix, and operator runbooks.

**Exit gate:** All design acceptance criteria pass; the restore exercise meets RPO <= 15 minutes and RTO <= 60 minutes; target load shows bounded memory, no lease oversell, and no account-limit violation.

## Program-Wide Commit and Review Rules

- Write a failing test before each behavior change.
- Keep commits limited to one coherent behavior or infrastructure gate.
- Require two reviewers for authentication, authorization, secret handling, tenant scoping, proxy policy, and lease logic.
- Do not merge a phase with skipped security, isolation, migration, or end-to-end gates.
- Update the compatibility matrix whenever AJiaSu, API, gRPC, database, Compose, or Helm compatibility changes.
