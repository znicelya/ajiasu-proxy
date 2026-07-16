# ADR 0004: Scheduler lease and fencing boundary

## Status

Accepted for Phase 6.

## Context

Pool endpoint allocation can be attempted by multiple control-plane replicas.
PostgreSQL owns durable business state, while Redis is required for short-lived
coordination. A paused or partitioned replica must not overwrite a newer
assignment after its lease expires.

## Decision

- PostgreSQL committed assignment rows are authoritative.
- Redis leases identify a temporary owner and allocate monotonically increasing
  fencing tokens.
- Endpoint, pool, account, and node leases are acquired in stable sorted order.
- Every assignment, capacity, desired Runner, and route-outbox mutation checks
  the fencing token in PostgreSQL.
- Losing renewal immediately removes local mutation authority. Release checks
  owner and token and cannot remove a newer lease.
- Redis degradation blocks new pool allocation, failover, and migration. It
  does not revoke existing committed routes with unexpired grants.
- Fixed endpoints never migrate automatically. Pool endpoints move only after
  a replacement Runner is observed and the new assignment is committed.

The scheduler protocol transports only opaque IDs, generations, fencing token
numbers, health categories, and safe reason codes. It never transports Redis
keys or values, account credentials, target addresses, route tickets, Docker
identifiers, or arbitrary diagnostics.

## Consequences

All scheduler writes require lease-aware transaction helpers and explicit stale
token errors. Redis recovery must reconcile against PostgreSQL before allocation
resumes. Failure tests must cover lease expiry before and after commit,
duplicate/out-of-order events, and competing replicas.
