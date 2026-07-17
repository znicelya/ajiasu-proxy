# Phase 9 Console, Operations, Performance, and Release Hardening Design

## 1. Goal

Phase 9 turns the stable management and deployment contracts into an operator
product: an accessible React console, production observability assets,
recoverable backup and restore evidence, a 10,000-connection load suite, and
signed/provenance-aware release artifacts. It must preserve the tenant,
authorization, secret, scheduler, proxy, Compose, and Helm boundaries from
Phases 1-8.

## 2. Scope and task boundaries

### Task 1: React console

Deliver a standalone `console/` React application using Fluent UI React
components. It consumes `/api/v1` only through a typed client, never stores
credentials or secret payloads in browser storage, and renders tenants,
members, accounts, pools, endpoints, operations, nodes, health, quotas, and
audit with loading, empty, error, pagination, optimistic-concurrency, and
reauthentication states.

### Task 2: Observability and audit export

Deliver Prometheus recording/alert rules, Grafana dashboard JSON, OpenTelemetry
configuration/export documentation, and a redacted SIEM audit export contract.
Metrics must not contain tenant secrets, proxy credentials, target hosts, or
unbounded user labels.

### Task 3: Recovery and capacity evidence

Deliver PostgreSQL PITR/keyring backup guidance, a restore rehearsal script and
machine-readable evidence, plus a 10,000-connection load harness that checks
bounded memory, lease oversell, account limits, and latency/error thresholds.

### Task 4: Release hardening and runbooks

Deliver image signing/provenance/SBOM CI contracts, a compatibility matrix,
release-note template, and operator runbooks covering install, upgrade,
rollback, incident response, backup/restore, and security boundaries.

## 3. Console information architecture

The left rail contains Overview, Tenants, Members, Accounts, Pools,
Endpoints, Operations, Nodes, Health, Quotas, and Audit. Detail pages preserve
the tenant ID in every route and request. Destructive actions require an
explicit confirmation and expected version; conflict responses explain how to
refresh without overwriting another operator's change.

The visual system uses Fluent UI tokens, one neutral enterprise theme, a single
teal action accent, 8px spacing rhythm, visible focus rings, reduced-motion
fallbacks, and responsive layouts from 320px to wide desktop. Dense data uses
virtualized or paginated tables rather than nested cards.

## 4. Observability contracts

Metrics use bounded dimensions such as component, protocol, state, result code,
and deployment revision. Required signals include request rate/error/latency,
active connections, scheduler lease contention, assignment state, account
capacity, node/session health, migration status, and backup age. Alert rules
cover readiness loss, lease contention, quota rejection spikes, Redis degraded
mode, stale snapshots, failed migrations, backup age, and restore failures.

Audit export is append-only, redacted, tenant-scoped where required, and
delivered through an operator-selected HTTPS/SIEM sink. Export retries are
idempotent and never block the request path.

## 5. Recovery and performance targets

The restore rehearsal must demonstrate RPO <= 15 minutes and RTO <= 60 minutes,
including the PostgreSQL data and matching encryption keyring. Redis and
ephemeral Runner state are reconstructed. The load suite targets 10,000
concurrent connections with no lease oversell, no account-limit violation,
bounded memory growth, and documented p95/p99 latency and error budgets.

## 6. Release and compatibility policy

Every release publishes image digests, SBOM, provenance attestation, signature
verification instructions, schema/protocol/Compose/Helm compatibility, and
rollback notes. CI rejects unsigned or mutable images in release manifests and
fails when compatibility metadata is missing.

## 7. Exit criteria

Phase 9 exits when all four tasks pass their tests and evidence gates, the
restore rehearsal meets RPO/RTO, the load suite meets safety limits, the
console is tenant-isolated and keyboard-accessible, and release artifacts are
verifiable without exposing secrets.
