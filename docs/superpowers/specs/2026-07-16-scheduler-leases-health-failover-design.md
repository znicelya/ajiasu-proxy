# Phase 6 Scheduler, Leases, Health, and Failover Design

## 1. Goal

Phase 6 extends the Phase 5 fixed-endpoint data plane with deterministic pool
allocation and safe lifecycle recovery. A pool-bound endpoint can select an
eligible account, node, and Runner without overselling account or node
capacity. Concurrent control-plane replicas coordinate through Redis leases
with fencing tokens, while PostgreSQL remains the committed assignment
authority.

The phase adds health signals, debounce and quarantine rules, bounded Runner
rebuilds, node migration, account replacement, cooldowns, and exponential
backoff. Existing safe traffic is preserved during control-plane or Redis
degradation; unsafe new allocations are blocked until ownership is known.

## 2. Scope and exclusions

Included:

- `binding_mode=fixed` and `binding_mode=pool` endpoint assignments.
- Pool strategies `least_connections`, `round_robin`, and `fixed_priority`.
- Deterministic candidate filtering by tenant, selector, lifecycle, health,
  node maintenance/connectivity, account capacity, and membership state.
- Redis leases, monotonic fencing tokens, renewal, expiry, ownership loss, and
  PostgreSQL committed assignment state.
- Process, tunnel, egress, and account health with consecutive success/failure
  thresholds, cooldowns, and quarantine.
- Bounded rebuild, account replacement, node migration, and stale-assignment
  cleanup.
- Gateway route deltas for assignment changes and safe Redis-degraded mode.

Explicitly excluded:

- Kubernetes scheduling, Helm, Compose packaging, and multi-region placement;
  those remain Phases 7 and 8.
- Global traffic shaping across independent Gateway installations. Phase 5's
  exact aggregate counters still require one active Gateway.
- TLS interception, proxy protocol expansion, SOCKS5 UDP/BIND, arbitrary
  policy languages, and account credential export.
- Seamless migration of established TCP streams. New connections use a new
  assignment after publication; established streams drain or fail normally.
- Redis as the source of truth for business state.

## 3. Invariants

1. PostgreSQL committed assignment state is authoritative for endpoint-to-
   account/node/Runner identity.
2. A Redis lease is required before mutating a pool assignment or starting a
   replacement. Every mutation carries a fencing token.
3. A lower fencing token cannot change assignment, desired Runner state,
   quarantine, or route publication.
4. Account reservations never exceed `max_concurrency`; nodes never exceed
   `max_runners-reserved_headroom`.
5. Candidates are tenant-scoped and eligible only when account, membership,
   node, capacity, cooldown, and health permit new traffic.
6. A route is published only after committed assignment, current Runner
   observation, and current route grant agree.
7. Duplicate and reordered work converges by endpoint, desired generation,
   operation ID, and fencing token.
8. Logs, traces, metrics, and audit details contain safe categories and opaque
   IDs only, never credentials, lease values, route tickets, targets, or DNS
   answers.

## 4. Assignment model

### 4.1 Endpoint binding

Migration 11 changes the Phase 4 endpoint constraint from fixed-only to:

- `fixed`: the existing account and node are authoritative.
- `pool`: `pool_id` is required and the scheduler owns the current assignment.

Endpoint identity, proxy credentials, access policy, and tenant URL remain
stable. A pool endpoint's selected account/node/Runner lives in a committed
assignment row rather than being accepted from a tenant request.

### 4.2 Assignment state machine

```text
unassigned -> acquiring -> assigned -> draining -> releasing -> unassigned
                  |             |          |
                  v             v          v
                blocked       failed     migrating
                  ^             |          |
                  +-------------+----------+
```

- `acquiring`: leases are held and account/node capacity is being reserved.
- `assigned`: committed assignment, Runner generation, and Gateway route are
  current.
- `draining`: no new traffic is routed after replacement publication;
  established streams may finish until a bounded deadline.
- `migrating`: old and replacement resources are tracked under one operation.
- `blocked`: no safe candidate or required dependency is available.
- `failed`: bounded retries are exhausted pending operator action or a new
  health transition.

## 5. Scheduler

### 5.1 Candidate eligibility

The scheduler evaluates pool candidates in this fixed order:

1. Same tenant and active, non-expired pool membership.
2. Active account, matching pool selector, and available concurrency.
3. Account not unhealthy, quarantined, or in cooldown.
4. Active, connected node below reserved headroom.
5. Required architecture/runtime capability is present.
6. Process, tunnel, and egress health are not disqualifying.

The scheduler never decrypts credentials during candidate selection. Capacity
uses the Phase 3 reservation primitive and is released only by the committed
owner or audited expiry recovery.

### 5.2 Strategies

- `least_connections`: lowest effective active connections, account reserved
  ratio, membership priority, then account ID.
- `round_robin`: durable pool cursor over sorted eligible memberships; advance
  only after assignment commit.
- `fixed_priority`: lowest membership priority, best health, reserved ratio,
  then account ID.

All tie-breakers are explicit. Retrying one generation/token chooses the same
candidate unless that candidate has become ineligible.

## 6. Redis leases and fencing

### 6.1 Lease keys

```text
ajiasu:lease:v1:endpoint:{tenant_id}:{endpoint_id}
ajiasu:lease:v1:pool:{tenant_id}:{pool_id}
ajiasu:lease:v1:node:{node_id}
ajiasu:lease:v1:account:{tenant_id}:{account_id}
```

Values contain an opaque owner, fencing token, acquisition time, and expiry.
Lua scripts atomically acquire, renew, and release. TTL is short, renewal runs
at one-third of TTL, and a missed renewal immediately removes local authority.
Release verifies owner/token and cannot delete a newer lease.

The fencing token is committed with assignment and operation rows. Stale
tokens are rejected by PostgreSQL before capacity, desired state, or route
outbox changes.

### 6.2 Commit protocol

1. Acquire endpoint, pool, account, and node leases in stable sorted order.
2. Lock endpoint assignment and relevant capacity rows.
3. Verify generation, fencing token, health, membership, and capacity.
4. Reserve capacity and commit assignment/operation state.
5. Append the assignment outbox event in the transaction.
6. Reconcile Runner state and publish a route after current observation.
7. Release leases after the protected operation completes.

Lease loss before commit rolls back. Lease loss after commit stops local
mutation; another owner reconciles from PostgreSQL.

### 6.3 Redis degraded mode

When Redis is unavailable or fencing responses are invalid:

- Existing assignments with current PostgreSQL state and unexpired grants may
  continue serving traffic.
- New pool allocation, migration, failover, and capacity reservations are
  blocked with `scheduler_coordination_unavailable`.
- Fixed endpoints continue only already committed safe behavior.
- Recovery first reconciles Redis ownership against PostgreSQL. Unknown
  ownership is never guessed.

## 7. Health and quarantine

Health dimensions are:

- `process`: Runner is running at expected generation.
- `tunnel`: Gateway-to-Agent relay opens and carries bounded health traffic.
- `egress`: Runner-side controlled probe succeeds without logging the target.
- `account`: AJiaSu session/authentication health.

Each dimension stores state, consecutive successes/failures, transition time,
safe reason category, and cooldown deadline. Default thresholds are:

- process: degrade after 2 failures, unhealthy after 3;
- tunnel/egress: degrade after 3, unhealthy after 5;
- account: quarantine after 3 authentication/session failures;
- recovery: 3 consecutive successes after cooldown.

Thresholds are deployment configuration, not tenant policy. Stale-generation
reports and duplicates do not advance counters.

An unhealthy account leaves new candidate selection and receives exponential
cooldown with a maximum. Existing assignments drain only after a replacement
is committed or the Runner is unsafe. Operator force-release is audited and
cannot bypass fencing or capacity.

Node loss blocks new assignments and creates bounded migration work for pool
endpoints. Fixed endpoints remain blocked rather than silently moving.

## 8. Failover and migration

Failover is a replacement assignment, not an in-place stream mutation:

1. Mark current assignment draining and stop issuing new grants.
2. Acquire leases with a higher fencing token.
3. Reserve replacement capacity and create a new Runner generation.
4. Wait for current running observation and a valid route grant.
5. Commit replacement and publish one ordered Gateway delta.
6. Release old capacity and enqueue old Runner cleanup.

Rebuild, account replacement, and node migration have separate bounded retry
budgets and cooldowns. Exhaustion writes a stable failure code and audited
outcome.

## 9. Gateway and API contracts

Route snapshots/deltas gain safe assignment metadata: assignment generation,
selected account/node/Runner opaque IDs, health category, and validity
deadline. They never include credentials, Redis keys, fencing secrets, Docker
identifiers, or target data.

Gateway rejects stale assignment generation, snapshot order, Runner
generation, policy hash, protocol, or grant expiry. Revoked/draining routes
stop new connections; established streams retain their own idle/byte limits.

Tenant routes:

```text
GET   /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/assignment
POST  /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/assignment/reconcile
GET   /api/v1/tenants/{tenant_id}/account-pools/{pool_id}/health
POST  /api/v1/tenants/{tenant_id}/account-pools/{pool_id}/reconcile
```

Endpoint create/update accepts `binding_mode=pool` and `pool_id`. Operators may
request reconcile but cannot directly choose an account/node for a pool
endpoint.

Audit/outbox events cover assignment commit/release, lease loss, capacity
rejection, health transition, quarantine/recovery, node migration, and retry
exhaustion. Details contain only IDs, generations, strategies, attempt counts,
and safe reason/state values.

## 10. Testing and exit criteria

Phase 6 is complete when:

1. Competing replicas cannot oversell account or node capacity.
2. Fencing rejects stale writes after expiry, renewal races, and Redis restart.
3. Fixed and pool endpoints behave deterministically for all strategies.
4. Duplicate, delayed, and reordered assignment/health events converge.
5. Redis loss blocks unsafe new allocations while preserving safe traffic.
6. Debounce, quarantine, cooldown, rebuild, migration, and account replacement
   produce deterministic audited outcomes.
7. Cross-tenant assignments, health, leases, deltas, logs, metrics, and errors
   remain isolated.
8. Gateway rejects stale assignment generations and drains revoked routes.
9. Migration up/down/up, PostgreSQL/Redis restart, protocol compatibility,
   race, vet, staticcheck, and contention/load gates pass.
10. No Phase 7/8 packaging or Phase 9 console scope is introduced.
