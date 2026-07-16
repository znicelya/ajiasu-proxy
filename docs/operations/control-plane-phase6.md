# Control Plane Phase 6 Operations

Phase 6 adds pooled endpoint scheduling, Redis leases with fencing tokens,
health debounce, account quarantine, bounded failover, and Gateway assignment
convergence. PostgreSQL remains authoritative. Redis contains only temporary
coordination state and must never be restored as business data.

## Required configuration

Configure Redis independently from PostgreSQL:

| Variable | Purpose | Operational constraint |
| --- | --- | --- |
| `AJIASU_REDIS_ADDRESS` | Redis `host:port` | Use a stable HA endpoint, not a Pod IP. |
| `AJIASU_REDIS_USERNAME` | ACL username | Grant only AUTH, SELECT, EVAL, GET, SET, DEL, INCR, and PEXPIRE capabilities for the scheduler namespace. |
| `AJIASU_REDIS_PASSWORD_FILE` | ACL password file | Mount as a secret; never place the password in an environment variable or log. |
| `AJIASU_REDIS_DATABASE` | Logical database | Keep scheduler coordination isolated from unrelated eviction policies. |
| `AJIASU_REDIS_TLS` | Redis transport TLS | Enable for every non-loopback production connection. |
| `AJIASU_REDIS_OPERATION_TIMEOUT` | Per-command deadline | Must not exceed the renewal interval. |
| `AJIASU_SCHEDULER_LEASE_NAMESPACE` | Key namespace | Keep stable across replicas in one environment and distinct across environments. |
| `AJIASU_SCHEDULER_LEASE_TTL` | Ownership lifetime | Valid range is 3 seconds to 5 minutes. |
| `AJIASU_SCHEDULER_LEASE_RENEW_INTERVAL` | Renewal cadence | Must be less than half of the TTL. |

The tested development profile uses a 9-second TTL, a 2-second renewal
interval, and a 1-second Redis operation timeout. Production values must allow
for observed Redis failover latency while still bounding stale ownership.

## Source of truth and fencing

An assignment is usable only when PostgreSQL contains the committed current
assignment, the Runner observation matches its generation, and the Gateway has
published the matching route. Redis ownership alone never proves an
assignment. Every protected write must carry a fencing token no lower than the
token stored in PostgreSQL.

Do not edit lease keys, fencing counters, assignment generations, or capacity
reservations manually. A deleted or recreated fencing counter can allow an old
token to collide with a new owner. Recovery always reconciles from PostgreSQL
and acquires a higher token through the normal lease script.

## Redis degraded mode

Redis loss is a degraded condition, not a reason to drop safe traffic:

- existing connections retain the route, grant, idle, and byte limits captured
  when the connection opened;
- existing assignments may accept new connections only while their committed
  route and grant remain current and unexpired;
- new pool allocation, capacity reservation, migration, failover, and manual
  assignment reconcile fail with `scheduler_coordination_unavailable`;
- fixed endpoints remain on their committed account and node and never migrate
  automatically.

Recovery procedure:

1. Confirm PostgreSQL schema 11 is ready and assignment rows are readable.
2. Restore the Redis HA endpoint and ACL/TLS configuration. Do not restore a
   lease-key backup and do not seed keys manually.
3. Confirm scheduler replicas can acquire and release a disposable test lease.
4. Reconcile blocked pool endpoints through their assignment reconcile API.
5. Require Gateways with a delta gap to receive a complete snapshot before
   accepting later deltas.
6. Watch blocked-allocation, stale-assignment, and lease-loss signals until
   they return to baseline.

## Health, quarantine, and cooldown

Health dimensions are `process`, `tunnel`, `egress`, and `account`. The default
thresholds are process degradation after 2 failures and unhealthy after 3;
tunnel/egress degradation after 3 and unhealthy after 5; account quarantine
after 3 authentication/session failures; and recovery after 3 successes once
cooldown has expired.

Duplicate, reordered, or stale-generation observations do not advance the
counters. Quarantine uses exponential cooldown. Investigate credentials and
provider status before releasing quarantine; repeated manual release without a
root-cause fix creates migration churn and may exhaust the retry budget.

## Failover, account replacement, and node drain

Pool failover is a replacement assignment:

1. mark the old assignment draining and stop new grants;
2. acquire higher fenced leases;
3. reserve replacement account/node capacity;
4. start and observe the replacement Runner at the new generation;
5. publish the replacement route;
6. release old capacity and clean up the old Runner.

Never delete the old Runner before the replacement route is published. Runner
rebuild, account replacement, and node migration have independent retry
budgets. `migration_retry_exhausted` requires operator investigation rather
than an unbounded retry loop.

Cordoning a node blocks new placement. Draining migrates eligible pool
endpoints through the sequence above. Fixed endpoints remain blocked on the
original node until an operator explicitly changes their binding.

## Gateway convergence

Gateway snapshots and deltas are ordered by snapshot version and assignment
generation. A duplicate identical delta is idempotent. A version gap requires
a fresh snapshot. A lower assignment generation, mismatched Runner generation,
expired grant, mismatched policy hash, draining state, or expired assignment
validity prevents new connections. Existing streams keep their cloned route
state and close under their normal limits.

The Phase 5 one-active-Gateway constraint still applies when exact aggregate
connection or traffic-window limits are required. Phase 6 coordinates
scheduler ownership; it does not yet provide Redis-backed global traffic
counters across multiple Gateway instances.

## Alerts and bounded diagnostics

Alert on bounded reason categories and IDs only:

- lease loss or coordination unavailable;
- blocked pool allocation or capacity exhausted;
- stale assignment generation or repeated snapshot recovery;
- account quarantine and quarantine duration;
- node offline with pool assignments pending migration;
- migration retry exhaustion;
- Gateway route validity/grant expiry and usage flush lag.

Do not put Redis keys or values, credentials, route signatures, proxy targets,
DNS answers, usernames, or raw upstream errors in logs, traces, alerts, or
metric labels.

## Rollback

Stop new Phase 6 writes before rollback. Drain active migrations, retain
PostgreSQL backups, and verify there are no pool endpoints or scheduler rows
that the previous application cannot interpret. The control plane requires an
exact schema version, so schema 11 and a schema-10 application must not run as
a mixed rolling set. Rehearse `10 -> 11 -> 10 -> 11` in a disposable database
before approving a rollback window.

Run `powershell -NoProfile -File scripts/scheduler-ci.ps1` before deployment.
Use `-SkipDocker` only for a local fast gate; release CI must run the Docker-
backed PostgreSQL contention and migration tests.
