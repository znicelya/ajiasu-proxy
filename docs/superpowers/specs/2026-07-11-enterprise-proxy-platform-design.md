# AJiaSu Enterprise Proxy Platform Design

**Date:** 2026-07-11  
**Status:** Approved design, pending written-spec review  
**Scope:** Enterprise multi-tenant proxy platform built around the public AJiaSu Linux CLI

## 1. Purpose

Transform the existing single-container AJiaSu wrapper into an enterprise proxy platform that supports:

- Docker Compose and Kubernetes deployments with the same application semantics.
- Up to 100 tenants, 1,000 AJiaSu accounts, 5,000 logical proxy endpoints, and approximately 10,000 concurrent TCP connections.
- HTTP forward proxy, HTTPS CONNECT, and SOCKS5 TCP CONNECT entry protocols.
- Fixed account/node bindings and policy-driven account-pool scheduling.
- Tenant isolation, RBAC, OIDC SSO, local break-glass administration, encrypted credentials, quotas, audit logs, monitoring, and controlled failover.

The platform wraps AJiaSu through its documented CLI, configuration files, process lifecycle, and observable network behavior. It does not modify, reverse engineer, or redistribute altered AJiaSu binaries.

## 2. Scope Boundaries

### 2.1 Included in the first production release

- Enterprise multi-tenancy for internal departments or teams.
- Local break-glass administrator plus OIDC Authorization Code with PKCE.
- Platform and tenant RBAC.
- AJiaSu account inventory, encrypted credentials, account pools, health, membership expiration, and configurable concurrent-login limits.
- Stable logical proxy endpoints with separately issued proxy credentials.
- HTTP, HTTPS CONNECT, and SOCKS5 TCP proxy access.
- Rust proxy gateway and node agent/data-plane components.
- Go modular-monolith control plane.
- React/TypeScript web console.
- PostgreSQL as the authoritative data store and Redis for leases, rate counters, and reconstructable caches.
- Docker Compose packaging and Kubernetes Helm packaging.
- Prometheus metrics, JSON logs, OpenTelemetry traces, audit export, backup, recovery, and upgrade procedures.

### 2.2 Explicitly excluded from the first production release

- Public SaaS registration, subscription plans, billing, and payment processing.
- Anonymous proxy access.
- TLS interception or content inspection.
- SOCKS5 UDP ASSOCIATE, transparent proxying, TUN client access, WireGuard, or OpenVPN entry protocols.
- Cross-region active-active operation.
- Preservation or transparent migration of established TCP connections during a data-plane failure.
- Kubernetes CRDs as a prerequisite for core behavior.
- Modification or reverse engineering of the AJiaSu executable.

## 3. Architecture Decision

Use a modular Go control plane with independently deployable Rust data-plane processes. Do not begin with a fully distributed microservice control plane or a Kubernetes-operator-centric design.

```text
Operators and tenant users
          |
          +--> React Web Console / versioned management API
                              |
                              v
                    Go Control Plane
                  /        |         \
          PostgreSQL     Redis     Secret Provider
                              |
                    desired state, leases,
                    encrypted credential delivery
                              |
                              v
                     Rust Node Agent
                              |
                    isolated AJiaSu Runners
                              ^
                              |
Clients --> Rust Proxy Gateway --> selected Runner --> AJiaSu network --> target
           HTTP / CONNECT / SOCKS5
```

This structure keeps the initial operational burden controlled while establishing interfaces that can later be extracted into services if measured scaling or organizational needs justify it.

## 4. Component Responsibilities

### 4.1 Go Control Plane

The control plane is one deployable service with internal modules and explicit interfaces:

- `identity`: OIDC identities, local break-glass identities, sessions, and service identities.
- `tenancy`: tenants, memberships, RBAC, and quotas.
- `accounts`: AJiaSu accounts, credential envelopes, login limits, membership metadata, and health.
- `pools`: account pools, pool membership, filters, and scheduling policies.
- `endpoints`: logical proxy endpoints, protocols, proxy credentials, access rules, and binding modes.
- `scheduler`: capacity evaluation, leases, allocation, and rescheduling.
- `nodes`: agent registration, heartbeats, labels, capacity, and maintenance state.
- `reconciler`: convergence of desired database state and reported data-plane state.
- `audit`: append-only security and operational events plus external export.
- `observability`: metrics, tracing, diagnostics, and health endpoints.

Internal modules must not bypass each other's authorization, validation, transaction, or audit boundaries through direct table manipulation.

### 4.2 React Web Console

- Uses only the versioned management API.
- Implements no authoritative authorization decisions.
- Provides tenant, account, pool, endpoint, node, operation, health, quota, and audit views appropriate to the caller's role.
- Never displays stored AJiaSu passwords or previously issued proxy secrets.

### 4.3 Rust Node Agent

- Runs on each eligible data-plane node.
- Receives desired Runner state over a versioned authenticated gRPC interface.
- Creates, stops, rebuilds, and garbage-collects Runners.
- Reports capacity, Runner state, health, and operation outcomes.
- Is the only platform component allowed to interact with the local container or pod lifecycle used for Runners.
- Treats all commands as idempotent by operation ID.

### 4.4 AJiaSu Runner

- Represents one active AJiaSu connection and one isolation boundary.
- Owns a distinct network namespace, cache directory, configuration, health state, and process lifecycle.
- Never serves more than one tenant.
- Receives decrypted credentials only during startup through memory or a restricted temporary file.
- Receives only capabilities proven necessary for AJiaSu. `privileged: true` is prohibited as a default.

### 4.5 Rust Proxy Gateway

- Accepts HTTP forward-proxy traffic, HTTPS CONNECT, and SOCKS5 TCP CONNECT.
- Authenticates a proxy-specific credential rather than a management credential.
- Resolves a stable logical endpoint to an eligible active Runner.
- Enforces source, target, port, connection, rate, idle-timeout, and traffic-quota policies.
- Does not decrypt TLS or record request/response bodies.

### 4.6 State and Secret Dependencies

- PostgreSQL is authoritative for configuration, allocations, operations, audit indexes, and desired state.
- Redis contains only short-lived leases, rate counters, and caches reconstructable from PostgreSQL and Agent reports.
- The default secret provider uses envelope encryption with a deployment-supplied master key.
- Production deployments may replace the default provider with Vault or a cloud KMS without changing account APIs or database semantics.

## 5. Multi-Tenant and Authorization Model

`Tenant` is the highest business isolation boundary. Every AJiaSu account, pool, endpoint, proxy credential, policy, quota, allocation, and audit event belongs to exactly one tenant.

Every tenant-scoped database operation must carry a server-derived `tenant_id`. Client-supplied resource identifiers cannot determine scope by themselves. Cache keys, metrics access, events, diagnostics, and background tasks must retain the same tenant boundary.

Roles are:

- `platform_admin`: platform configuration, tenant lifecycle, nodes, and platform operations. This role does not receive plaintext tenant credentials.
- `tenant_admin`: members, accounts, pools, endpoints, policies, proxy credentials, and quotas within one tenant.
- `operator`: endpoint lifecycle, switching, and diagnostics without credential read/export access.
- `auditor`: read-only configuration, status, and audit access.
- `consumer`: use of explicitly granted proxy endpoints only.

Users may belong to multiple tenants through explicit `Membership` records. Authorization is evaluated on every request and background action, not inferred from the web interface.

## 6. Identity and Credential Security

### 6.1 Management identity

- OIDC uses Authorization Code with PKCE.
- External identities bind to the immutable pair `issuer + subject`, never email alone.
- Local break-glass access is disabled remotely by default and enabled only through deployment configuration.
- Break-glass accounts require a strong password, TOTP, rate limiting, and audit logging.
- Workload-to-workload authentication uses short-lived identities. Kubernetes should use workload identity where available; Compose uses rotatable service tokens.

### 6.2 Proxy credentials

- Proxy credentials are unique to an endpoint or explicit grant and never reuse management passwords.
- HTTP/HTTPS uses Proxy Authentication; SOCKS5 uses username/password authentication.
- Only a slow, salted, one-way verifier is stored.
- Plaintext is returned once at creation or rotation and cannot be retrieved afterward.
- A credential may have an expiration, revocation state, allowed source CIDRs, target rules, port rules, rate limits, and concurrent-connection limits.
- Anonymous access is rejected.

### 6.3 AJiaSu credentials

- Each encrypted record uses an independent nonce and authenticated context containing at least tenant and account identifiers.
- In the default backend, the data-encryption key is wrapped by a deployment master key.
- In Vault/KMS mode, the database stores ciphertext or an external secret reference.
- Passwords and tokens are forbidden in logs, audit detail, traces, error responses, operation messages, and metric labels.
- Platform administrators may operate resources without gaining a plaintext credential retrieval path.

## 7. Proxy Endpoint and Protocol Semantics

A `ProxyEndpoint` has a stable identity, belongs to one tenant, and exposes HTTP/HTTPS CONNECT, SOCKS5, or both. It does not expose an internal Runner address to clients.

Binding modes are:

- `fixed`: binds to a designated AJiaSu account and optionally constrained nodes.
- `pool`: selects an account and node from an account pool according to policy.

HTTP mode supports standard forward proxy requests. HTTPS uses CONNECT tunneling without TLS decryption. SOCKS5 supports TCP CONNECT only in the first release.

DNS policy must be explicit per endpoint: local controlled resolution or resolution through the selected AJiaSu path. The implementation must prevent accidental DNS leakage when egress-side resolution is required.

Default traffic policy denies loopback, link-local, platform management networks, and cloud metadata endpoints. Tenant rules may further allow or deny destination CIDRs, domains, and ports. Platform safety denies cannot be weakened by a tenant.

## 8. Scheduling and Account Concurrency

Each account has a configurable maximum number of active AJiaSu connections. The default is one.

Scheduling considers:

- Tenant and account-pool membership.
- Account concurrent-login capacity and active leases.
- Account health and membership expiration.
- Node labels, maintenance state, Runner capacity, and reserved headroom.
- Endpoint region constraints and preferred priorities.
- Current Runner and connection load.

The first release supports least-connections, round-robin, and fixed-priority strategies.

Allocation uses distributed leases with fencing tokens. PostgreSQL records desired allocations and committed assignment state. Redis accelerates short-lived lease coordination. If Redis is unavailable, the system stops new pool allocations but allows existing traffic and fixed assignments to continue where safe. It must never fall back to uncoordinated scheduling that can exceed an account limit.

Scheduling is idempotent and produces an auditable explanation of selected and rejected candidates.

## 9. Health and Failure Handling

Health evaluation has four layers:

- Process: AJiaSu process is running.
- Tunnel: expected interface, route, or local proxy behavior exists.
- Egress: a controlled probe can reach an approved target and optionally validate egress IP or region.
- Account: login status, service rejection, membership expiration, and consecutive failures.

Health transitions require configurable consecutive success/failure thresholds to reduce flapping.

Failure behavior:

1. An Agent first attempts a bounded rebuild under the same account lease.
2. If the node is unavailable, a fixed endpoint may move to another eligible node and a pool endpoint is rescheduled.
3. Existing TCP connections may terminate; clients reconnect through the stable endpoint.
4. Exponential backoff, cooldowns, retry ceilings, and account quarantine prevent restart storms.
5. Failover never bypasses tenant quota, account concurrency, destination policy, or region constraints.

The control plane and Gateway are multi-replica capable. The first release does not promise seamless migration of established connections.

## 10. Resource State and API Contract

Schedulable resources expose desired `spec`, observed `status`, a lifecycle state, and a resource version.

- API writes update desired state.
- Reconcilers update observed state asynchronously.
- API calls do not block until Runner startup completes.
- Optimistic concurrency rejects stale writes with a stable conflict error.
- Deletion marks a resource, withdraws assignments, cleans data-plane state, and only then finalizes deletion.
- Agent reports cannot overwrite user intent.

Management API constraints:

- JSON API under `/api/v1`, documented with OpenAPI 3.1.
- UUID resource identifiers and mandatory pagination for collections.
- Request IDs on all requests and idempotency keys for writes that may be retried.
- Stable machine-readable error codes without internal stack traces or upstream secrets.
- Long-running actions return an `Operation` resource.
- Bulk account import validates before execution and returns an explicit per-item result report.
- Credential creation returns plaintext once; later calls can only rotate or revoke it.

Agent communication uses a versioned authenticated gRPC contract that supports rolling compatibility between the current and immediately previous protocol versions.

## 11. Consistency and Background Processing

- A business-state change and its publishable event use a PostgreSQL transaction Outbox.
- The first release consumes Outbox work with database-backed workers; Kafka or NATS is not mandatory.
- Redis loss does not lose authoritative configuration.
- Reconcilers rebuild work after restart from desired state and Agent reports.
- No correctness-critical queue exists only in process memory.
- Configuration changes follow validation, authorization, persistence, audit, and asynchronous reconciliation.

## 12. Deployment Design

### 12.1 Docker Compose

- Provides development and production examples.
- Includes the control plane, console, PostgreSQL, Redis, Gateway, Agent, and Runner execution support.
- Uses separate persistent storage for PostgreSQL, audit data, AJiaSu cache, and encryption material.
- Includes database migration, health checks, graceful shutdown, backup, and restore commands.
- Contains no built-in production password or encryption key.
- Does not expose the Docker socket to the console or Gateway. Agent runtime access is narrowly scoped and documented.

### 12.2 Kubernetes and Helm

- Control Plane and Gateway use Deployments.
- Agent uses a DaemonSet when node-level network/runtime access is required.
- Runners use isolated Pods with restricted ServiceAccounts and security contexts.
- Helm supports replica counts, resources, scheduling constraints, disruption budgets, anti-affinity, topology spread, NetworkPolicy, and secret-provider configuration.
- PostgreSQL and Redis are external dependencies for production. Bundled instances, if offered, are development-only.
- Database changes use expand-migrate-contract sequencing and a migration/compatibility Job before application rollout.
- Core behavior does not require CRDs.

Compose and Kubernetes use the same images, APIs, database model, configuration names, and resource semantics.

## 13. Capacity and Availability Targets

Design and acceptance targets:

- Up to 100 tenants.
- Up to 1,000 AJiaSu accounts.
- Up to 5,000 logical proxy endpoints.
- Approximately 10,000 concurrent TCP connections.

Control Plane and Gateway are horizontally scalable. Gateway scaling uses connection count, connection rate, CPU, and memory. Agent capacity includes maximum Runners, active Runners, reserved headroom, and node resource availability.

Data-plane or node failure may interrupt current traffic but must not make the management control plane unavailable. Recovery point objective is at most 15 minutes and recovery time objective is at most 60 minutes for authoritative platform state.

## 14. Observability and Audit

All components emit structured JSON logs containing timestamp, level, component, request ID, tenant ID when authorized, resource ID, and stable error code. Sensitive identities and destinations are minimized or masked by policy.

Prometheus metrics cover:

- API latency and error rate.
- Authentication and authorization failures.
- Scheduling latency, candidate rejection, and lease conflict.
- Runner state, restarts, quarantine, and failover.
- Gateway active connections, connection rate, failures, timeouts, and byte totals.
- Node and account-pool capacity.
- Dependency health and Redis degraded mode.

Metric labels must avoid high-cardinality destination domains and IP addresses.

OpenTelemetry traces cover management requests, scheduling, reconciliation, and Agent operations. Proxy content is never traced.

Audit records include actor, tenant, action, resource, outcome, source IP, request ID, and timestamp for login, permission changes, credential operations, account imports, endpoint changes, scheduling, switching, and privileged administration. Records are append-only for application roles, retained for 180 days by default, and exportable to object storage or SIEM.

## 15. Backup and Recovery

- PostgreSQL receives scheduled backups and production point-in-time recovery where supported.
- Restore exercises are part of release and operations practice.
- Master encryption material is backed up separately under stricter access control; a database backup without its corresponding key is considered unrecoverable.
- Redis is excluded from critical backup requirements.
- Audit data may be archived externally according to retention policy.

## 16. Testing Strategy

### 16.1 Unit and property tests

- Go tests tenant scoping, RBAC, quotas, state machines, leases, reconciliation, and scheduling.
- Rust tests protocol parsing, authentication, route policy, timeouts, resource bounds, and malformed clients.
- Property tests cover parsers, policy composition, lease invariants, and resource accounting.

### 16.2 Integration and end-to-end tests

- PostgreSQL, Redis, and process coordination tests use real dependencies for correctness-critical behavior.
- Protocol tests cover HTTP forwarding, HTTPS CONNECT, SOCKS5 CONNECT, denial, timeout, half-close, and invalid inputs.
- Isolation tests attempt cross-tenant access through IDs, caches, events, metrics, diagnostics, logs, and errors.
- Failure tests cover Agent loss, Runner crash, node loss, Redis loss, database interruption, and duplicate delivery.
- End-to-end suites run against both Compose and a temporary Kubernetes cluster.
- Load tests verify approximately 10,000 concurrent connections, bursts, long-lived sessions, bounded memory, and recovery.

### 16.3 AJiaSu black-box contract tests

- Validate `login`, `list`, `connect`, exit behavior, configuration semantics, and critical output markers.
- Avoid depending on nonessential output formatting as the only state signal.
- Use a controlled substitute process in normal pull-request CI.
- Run real-account tests only in a protected environment with explicit authorization.
- Gate AJiaSu upgrades on compatibility, stability, and rollback tests.

## 17. Supply-Chain and Runtime Security

- AJiaSu version, architecture mapping, download URL, and SHA-256 checksum are explicit and immutable for each release.
- Builds fail on checksum mismatch and do not execute unchecked downloads.
- Base images are pinned by digest and updated through an audited automation process.
- Go, Rust, and frontend builds use multi-stage images with minimal non-root runtime images and read-only roots where possible.
- Runner capabilities are individually justified. Default privileged containers are prohibited.
- CI produces an SBOM, signs images, records provenance, and scans source, dependencies, secrets, licenses, and images.
- Initial enterprise releases target `linux/amd64` and `linux/arm64`. Legacy `386` and `arm/v7` require a separate support decision.
- Critical vulnerabilities, tenant-isolation failures, incompatible migrations, and material performance regressions block release.

## 18. Delivery and Quality Gates

- Go, Rust, and TypeScript enforce formatting, linting, strict compilation, and automated tests.
- Security-critical changes require two reviewers.
- Database migrations are tested forward and for rolling compatibility with the previous application version.
- OpenAPI and gRPC compatibility checks block breaking changes within the supported version window.
- Feature implementation follows test-driven development with small, independently verifiable commits.
- Release validation includes Compose smoke tests, Kubernetes installation and upgrade tests, backup/restore, and rollback rehearsal.

## 19. Acceptance Criteria

The first production release is acceptable only when:

1. Compose and Kubernetes use the same application images and resource semantics.
2. Automated tests find no cross-tenant access path through APIs, storage, caches, events, logs, metrics, or diagnostics.
3. Fixed and pooled endpoints can allocate, enforce limits, expose supported protocols, and fail over within policy.
4. Redis loss prevents unsafe new pool scheduling without destroying existing safe traffic.
5. A single Runner or node failure does not make the control plane unavailable.
6. The target load shows bounded memory, no lease overselling, and no account concurrency violation.
7. Sensitive operations generate append-only audit events and no plaintext secret appears in logs or telemetry.
8. AJiaSu artifacts are checksum-verified and platform images pass the release supply-chain gates.
9. Backup and restore meet the stated RPO and RTO in a documented exercise.

## 20. Delivery Decomposition

The platform is too large for one implementation plan. It must be delivered as independently testable sub-projects in this order:

1. Repository, secure AJiaSu Runner image, CI baseline, and black-box contract harness.
2. Control-plane foundation: PostgreSQL schema, tenant model, OIDC/local identity, RBAC, audit, and API conventions.
3. Account and secret management: encrypted credentials, accounts, pools, concurrency limits, and import.
4. Node Agent and Runner lifecycle with desired/actual-state reconciliation.
5. Rust Gateway with HTTP, HTTPS CONNECT, SOCKS5, credentials, and destination policy.
6. Scheduler, fencing leases, fixed/pool assignment, health, quarantine, and failover.
7. Compose production packaging and end-to-end validation.
8. Helm deployment, Kubernetes security, scaling, upgrade, and failure testing.
9. Web Console, operational dashboards, alerts, backup/restore, and release hardening.

Each sub-project receives its own implementation plan and must leave the repository in a working, testable state.

## 21. Existing Repository Findings

The repository currently contains only `Dockerfile` and `README.md` and had no Git metadata when this design began.

The current Dockerfile:

- Downloads AJiaSu 4.2.3.0 without verifying a cryptographic checksum.
- Copies the executable into a minimal Alpine image.
- Does not define a non-root user, health check, immutable base-image digest, metadata, or explicit runtime capability model.

The current README documents a single-account, host-network, privileged Compose workflow. That workflow is useful as behavioral reference but cannot be retained as the production platform security model.

## 22. Assumptions Requiring Early Validation

- The AJiaSu license and service terms permit the intended internal enterprise account orchestration and containerized execution.
- The Linux binary operates correctly on the chosen minimal runtime and supported CPU architectures.
- Account concurrency behavior can be represented by an administrator-configurable limit with a safe default of one.
- Required routing can be isolated per Runner without granting broad privileges to unrelated components.
- AJiaSu CLI and network behavior expose sufficient signals for reliable health and lifecycle management.

Failure of any assumption changes the implementation approach and must be resolved during sub-project 1 before higher-level platform work proceeds.
