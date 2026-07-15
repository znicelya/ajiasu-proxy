# Phase 6 Scheduler, Leases, Health, and Failover Implementation Plan

**Goal:** Add deterministic fixed/pool endpoint allocation, Redis leases with
fencing, health debounce and quarantine, bounded Runner migration/failover,
Gateway route convergence, Redis degraded mode, and Phase 6 operations gates.

**Entry dependency:** Phase 5 Gateway access, route-grant, Agent relay, policy,
credential, endpoint, account, pool, node, and Runner observation contracts
are merged and stable.

## Mandatory worktree and commit rule

Create one dedicated worktree for all ten tasks:

```powershell
git worktree add .worktrees/phase-6-scheduler-leases -b feat/phase-6-scheduler-leases main
```

Every task is independently reviewable. Write a failing test first, run the
task checks, commit the task, and leave the worktree clean before starting the
next task. Follow-up fixes use a new commit.

At the end of every task:

```powershell
git diff --check
git status --short
git add <task files>
git diff --cached --check
git commit -m "phase6(task-N): <short outcome>"
git status --short
```

## Execution rules

- PostgreSQL committed assignment state is authoritative; Redis coordinates
  owners only.
- Every protected mutation carries a fencing token and checks it in SQL.
- Acquire leases and database locks in a documented stable order.
- Use fake AJiaSu credentials and fake Runner/Agent/Gateway boundaries in CI.
- Never log Redis keys/values, credentials, route tickets, targets, DNS
  answers, or arbitrary upstream errors.
- Generated protobuf and sqlc files are regenerated, never hand-edited.
- Preserve existing safe traffic during Redis degradation and block unsafe new
  pool allocation.
- Fixed endpoints never silently migrate. Pool endpoints migrate only through
  committed replacement state and ordered Gateway deltas.

## Task 1: Freeze scheduler, fencing, and health contracts

**Files**

- Create `api/proto/scheduler/v1/scheduler.proto`.
- Extend Gateway route snapshot/delta assignment metadata.
- Add immutable revision fixtures.
- Create `docs/adr/0004-scheduler-fencing-boundary.md`.
- Add scheduler contract tests.

**Behavior**

- Define assignment intents, opaque resource IDs, generations, fencing tokens,
  health dimensions, and bounded reconcile outcomes.
- Reserve fields for later distributed counters and multi-region placement.
- Reject unsupported revisions and stale generations before mutation.

**Checks**

- Buf lint/breaking/generate when installed.
- Deterministic descriptor/fixture tests.
- Duplicate, delayed, and reordered event contract tests.

**Task commit:** `feat: freeze scheduler and fencing contracts`

## Task 2: Add schema 11

**Files**

- Create `migrations/00011_scheduler_leases_health.sql`.
- Add queries/sqlc outputs for assignments, health, cooldowns, migration
  attempts, and scheduler cursors.
- Update control-plane schema readiness.

**Behavior**

- Allow fixed and pool endpoint binding while preserving endpoint identity.
- Store committed assignment, desired generation, fencing token, capacity
  reservations, state, and transition timestamps.
- Store health dimension counters, safe reason codes, cooldowns, and bounded
  retry state.
- Apply forced RLS and tenant-safe platform policies.

**Checks**

- Migration `10 -> 11 -> 10 -> 11`, restart, RLS, and cross-tenant tests.
- sqlc generate, vet, and diff.

**Task commit:** `feat: add scheduler assignment and health schema`

## Task 3: Implement Redis leases and fencing

**Files**

- Create `internal/scheduler/lease.go`, Redis client interface, Lua scripts,
  and tests.
- Add Redis TTL, renewal, timeout, and degraded-mode configuration.

**Behavior**

- Acquire endpoint/pool/account/node leases in stable order.
- Allocate monotonic fencing tokens atomically.
- Renew only when owner/token match and detect ownership loss immediately.
- Release cannot remove a newer owner's lease.
- Map backend failures to stable coordination errors.

**Checks**

- Competing owners, renewal races, expiry, Redis restart, malformed replies,
  release races, and stale-token SQL write tests.
- Property test that one resource/token cannot produce two committed owners.

**Task commit:** `feat: add Redis leases and fencing tokens`

## Task 4: Implement deterministic candidate scheduling

**Files**

- Create `internal/scheduler/model.go`, `selector.go`, `strategy.go`, and tests.
- Reuse Phase 3 pool selectors, memberships, quotas, and reservations.

**Behavior**

- Filter by tenant, lifecycle, selector, membership expiry, health, cooldown,
  node state, architecture, and capacity.
- Implement least-connections, round-robin, and fixed-priority strategies with
  explicit tie-breakers.
- Reserve account/node capacity only while lease and transaction ownership are
  current.
- Return stable coordination/capacity/no-candidate errors.

**Checks**

- Strategy golden vectors, concurrent reservation, selector isolation,
  membership expiry, cooldown, node headroom, and account limit tests.

**Task commit:** `feat: add deterministic pool scheduler`

## Task 5: Add health debounce, quarantine, and cooldown

**Files**

- Create `internal/health` evaluator/state machine and tests.
- Modify Agent/Gateway adapters to emit bounded health observations.
- Add health audit/outbox events.

**Behavior**

- Track process, tunnel, egress, and account health independently.
- Apply configured consecutive failure/success thresholds and hysteresis.
- Quarantine accounts after bounded authentication/session failures.
- Mark nodes stale/offline without moving fixed endpoints.
- Ignore stale-generation and duplicate reports.

**Checks**

- Debounce vectors, recovery, quarantine cooldown, stale reports, duplicates,
  threshold validation, and cross-tenant health tests.

**Task commit:** `feat: add health debounce and quarantine`

## Task 6: Add pool endpoint assignment APIs

**Files**

- Modify endpoint model/service/http/OpenAPI for `binding_mode=pool` and
  `pool_id`.
- Create scheduler assignment service and HTTP endpoints.
- Add optimistic version/idempotency tests.

**Behavior**

- Create/update pool endpoints without accepting direct account/node choices.
- Reconcile under leases, reserve capacity, commit assignment, and create one
  idempotent operation.
- Reject unsafe binding changes during active operations/drains.
- Enforce tenant role matrix and safe audit/outbox details.

**Checks**

- Role, cross-tenant, stale version, retry, duplicate reconcile, binding,
  quota, and capacity tests.
- OpenAPI/registered-route consistency.

**Task commit:** `feat: add pool endpoint assignment APIs`

## Task 7: Integrate bounded rebuild, failover, and migration

**Files**

- Modify reconciler workers and Runner desired/observation flows.
- Extend Agent commands with assignment generation/fencing metadata.
- Add failover/migration operation tests.

**Behavior**

- Rebuild failed Runners under higher generation and fencing token.
- Migrate only pool endpoints after replacement capacity is committed.
- Quarantine failing accounts and select replacements after cooldown.
- Publish replacement route before old Runner cleanup.
- Converge duplicate/out-of-order commands without duplicate Runner creation.

**Checks**

- Runner stop, Agent/node/account loss, lease loss during every operation
  phase, restart recovery, retry exhaustion, and cleanup accounting.

**Task commit:** `feat: integrate bounded failover and migration`

## Task 8: Converge Gateway routes and Redis degraded mode

**Files**

- Modify Gateway route/control client and Agent relay authorization.
- Add assignment outbox consumption and route-cache recovery.
- Add degraded-mode tests.

**Behavior**

- Publish routes only for committed current assignments.
- Drain old pool routes after replacement; fixed routes remain fixed.
- Reject stale assignment, policy, Runner generation, grant, and fencing
  metadata.
- Preserve safe established traffic while blocking unsafe new allocation when
  Redis is unavailable.
- Reconcile PostgreSQL assignment and route cache after recovery.

**Checks**

- Duplicate/reordered deltas, Gateway restart, expired grants, Redis loss and
  recovery, route draining, and established-stream tests.

**Task commit:** `feat: add route convergence and degraded mode`

## Task 9: Add contention, failure, isolation, and load gates

**Files**

- Create Phase 6 integration, isolation, failure, and lease-contention tests.
- Add Redis/PostgreSQL testcontainers and deterministic fake boundaries.

**Coverage**

- Multiple replicas scheduling the same endpoint.
- Account/node capacity under contention.
- Lease expiry, fencing races, Redis restart/degraded mode, and recovery.
- Health debounce, quarantine, rebuild, migration, and account replacement.
- Cross-tenant assignment, health, route, log, metric, and error isolation.
- Duplicate, delayed, and reordered operations/events.

**Checks**

- Race/property tests, bounded goroutine/task counts, and deterministic
  audit/outbox assertions.

**Task commit:** `feat: add scheduler failure and contention gates`

## Task 10: Add operations and CI gates

**Files**

- Create `docs/operations/control-plane-phase6.md`.
- Create `scripts/scheduler-ci.ps1` and scheduler workflow.
- Update control-plane/Gateway CI, OpenAPI checks, and compatibility matrix.

**Behavior**

- Document Redis HA, TTL/renewal, degraded mode, fencing recovery, health
  thresholds, quarantine release, node drain, account replacement, rollback,
  and the Phase 5 one-Gateway exact-limit constraint.
- Add bounded alerts for lease loss, blocked allocation, stale assignment,
  quarantine, migration exhaustion, and usage lag.

**Checks**

```powershell
buf lint api/proto
buf breaking api/proto --against '.git#branch=main,subdir=api/proto'
buf generate api/proto
go tool sqlc generate
go tool sqlc vet
go tool sqlc diff
go mod tidy -diff
cargo fmt --all --check
cargo clippy --workspace --all-targets --all-features -- -D warnings
cargo test --workspace --all-features
cargo deny check
go test -race -p 1 ./...
go vet ./...
go tool staticcheck ./...
powershell -NoProfile -File scripts/scheduler-ci.ps1
git diff --check
git status --short
```

**Task commit:** `feat: add scheduler operations and CI gates`

## Final verification gate

Run the Task 10 checks plus:

- Redis restart/failover rehearsal against committed PostgreSQL assignments.
- PostgreSQL `10 -> 11 -> 10 -> 11` migration rehearsal.
- Gateway/Agent current and previous protocol compatibility checks.
- Multi-architecture builds for existing control-plane, Agent, Runner, and
  Gateway images when locked builders are available.

Expected: all available gates exit zero, every task has a separate commit, and
the Phase 6 worktree is clean before merging to `main`.
