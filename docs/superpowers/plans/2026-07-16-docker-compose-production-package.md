# Phase 7 Docker Compose Production Package Implementation Plan

**Goal:** Deliver a secure, reproducible Docker Compose package for the Phase
6 Control Plane, Gateway, Agent, and dynamic Runner path, including locked
images, file-backed secrets, lifecycle automation, backup/restore,
upgrade/rollback, and end-to-end validation.

**Entry dependency:** Phase 6 schema 11, scheduler leases/fencing, health,
Gateway assignment convergence, Agent Runner lifecycle, proxy protocols, and
operations contracts are merged and stable.

**Scope amendment:** Reserve an optional Console profile and configuration
contract, but do not implement or ship the Phase 9 Web Console.

## Mandatory worktree and commit rule

Use one dedicated worktree for all ten tasks:

```powershell
git worktree add .worktrees/phase-7-compose-package -b feat/phase-7-compose-package main
```

Every task begins with a clean worktree, writes a failing test or validation
fixture first, runs its checks, and ends with a separate commit. Follow-up
fixes after a task commit use another explicit commit and do not rewrite prior
task history.

At the end of every task:

```powershell
git diff --check
git status --short
git add <task files>
git diff --cached --check
git commit -m "phase7(task-N): <short outcome>"
git status --short
```

## Execution rules

- Do not stage, discard, normalize, or overwrite unrelated user changes in the
  main worktree.
- Generated state and secrets live only under ignored deployment directories
  with restrictive permissions.
- Production secrets use mounted files; no secret-bearing DSN or enrollment
  token is passed through Compose environment or command arguments.
- Pin application, build, runtime, and bundled dependency images by digest.
- Only the Agent may mount the Docker socket. Never use `privileged`, host
  network, host PID, or host IPC.
- Runners are created dynamically by the Agent and are not standing Compose
  services.
- Use fake AJiaSu, fake credentials, and controlled targets in normal CI.
- Preserve Phase 6 PostgreSQL/Redis authority and degraded-mode behavior.
- Do not add Helm/Kubernetes resources or the full Console implementation.
- Do not describe a stack as ready until a real packaged proxy smoke request
  passes.

## Task 1: Freeze the Compose and release contracts

**Files**

- Create `docs/adr/0005-compose-runtime-boundary.md`.
- Create `deploy/compose/release-manifest.schema.json`.
- Create `deploy/compose/configuration-matrix.yaml`.
- Create `deploy/compose/testdata/revision-1.json`.
- Create Compose/release contract tests under `tests/contract`.
- Update `docs/operations/compatibility-matrix.md`.

**Behavior**

- Define stable service names, ports, networks, volumes, secret mount paths,
  health outcomes, image manifest fields, schema/protocol revisions, and future
  Helm mappings.
- Reserve the optional `console` profile without an implementation image.
- Define supported host OS/architecture, Docker Engine, Buildx, and Compose
  versions.
- Reject mutable images, missing digests, unknown manifest revisions,
  duplicate published ports, and configuration names without an owner.

**Checks**

- JSON schema validation and immutable revision fixture tests.
- Golden configuration-matrix and compatibility assertions.
- Contract test proving no secret values/defaults appear in committed files.

**Task commit:** `phase7(task-1): freeze compose and release contracts`

## Task 2: Add file-backed secrets and lifecycle commands

**Files**

- Modify Control Plane configuration for database DSN files and deployment
  metadata.
- Modify Agent configuration for enrollment-token files, Docker socket path,
  and health/status commands.
- Add Gateway configuration/session persistence, enrollment-token files,
  health/status, and graceful-shutdown settings.
- Add `control-plane migrate up|status` and bounded health/version commands.
- Add config/CLI tests for all components.

**Behavior**

- Prefer `_FILE` for every secret-bearing value and reject conflicting direct
  and file values.
- Read secret files once with bounded size, safe permissions, and redacted
  errors; never log paths or content.
- Run migrations under a PostgreSQL advisory lock with explicit timeout and
  exact schema verification.
- Health/version commands are local, bounded, non-mutating, and secret-free.
- Agent/Gateway enrollment material is removed or made unusable after durable
  session state is established.

**Checks**

- Conflict, permission, oversize, symlink/directory, whitespace, and redaction
  tests.
- Concurrent migration-job exclusion and restart tests.
- SIGTERM/graceful-deadline tests for Control Plane, Agent, and Gateway.

**Task commit:** `phase7(task-2): add deployment-safe config and lifecycle commands`

## Task 3: Close the packaged Gateway and relay runtime path

**Files**

- Wire the Gateway control gRPC server into the Control Plane runtime.
- Implement the Gateway registration/control client and session persistence.
- Run Gateway HTTP/CONNECT/SOCKS listeners from `main` with bounded shutdown.
- Complete Gateway-to-Agent relay transport and Agent authorization wiring.
- Add current/previous protocol and restart convergence tests.

**Behavior**

- Gateway starts no public listener until registration, current snapshot, and
  route-grant verification are ready.
- Control Plane publishes only committed current assignments.
- Relay authorization checks Gateway audience, assignment/Runner generation,
  policy hash, protocol, validity, and signed grant before opening a Runner
  path.
- Control-stream loss preserves only safe unexpired routes and reconnects with
  full snapshot recovery after a version gap.
- Shutdown stops new connections, drains established streams, and bounds every
  background task.

**Checks**

- In-process and container tests for registration, session rotation, snapshot,
  duplicate/reordered delta, relay open, half-close, and shutdown.
- Negative tests for stale grant/generation, wrong audience, cross-tenant route,
  expired session, and unavailable Agent/Runner.

**Task commit:** `phase7(task-3): complete packaged gateway and relay runtime`

## Task 4: Lock and harden all release images

**Files**

- Create `build/compose-images.lock` and lock/update fixture scripts.
- Harden `Dockerfile.gateway` and `Dockerfile.agent`.
- Align Control Plane and Runner labels/health contracts.
- Add multi-architecture image-contract tests and SBOM inputs.
- Add a fake AJiaSu Runner Dockerfile for E2E tests only.

**Behavior**

- Pin Rust/Go builders, Alpine runtimes, PostgreSQL, Redis, and development
  identity-provider images by reviewed digest.
- Produce `linux/amd64` and `linux/arm64` application images with OCI metadata.
- Run non-root, use minimal writable paths, and contain no default credential.
- Agent image supports a configured Docker socket/group without privileged
  mode; fake Runner is impossible to select in production manifests.

**Checks**

- Lock parser/update rollback tests.
- Docker history, user, entrypoint, health, labels, capabilities, filesystem,
  architecture, SBOM, and vulnerability scan gates.
- Secret/canary search across image layers and metadata.

**Task commit:** `phase7(task-4): lock and harden compose release images`

## Task 5: Create canonical Compose profiles and topology

**Files**

- Create `deploy/compose/compose.yaml`.
- Create dependency, development, and production overlays.
- Create non-secret environment/configuration examples.
- Add rendered-model validation and security tests.
- Update `.gitignore`/`.dockerignore` for generated deployment state.

**Behavior**

- Stable service names for migration, Control Plane, Gateway, and Agent.
- Optional pinned PostgreSQL/Redis/Keycloak dependencies by profile.
- Separate edge, control, and dependency networks.
- Publish only documented Gateway ports and loopback management ports by
  default; bundled database/Redis ports remain private.
- Mount Docker socket only into Agent, with explicit group mapping.
- Use read-only roots, dropped capabilities, `no-new-privileges`, bounded
  tmpfs/volumes, health checks, restart policies, and stop-grace periods.
- Define no standing Runner service and no enabled Console service.

**Checks**

- `docker compose config --quiet` for every supported overlay combination.
- Model tests reject socket leaks, privileged/host modes, mutable images,
  public dependencies, secret environment values, missing health checks, and
  duplicate ports.

**Task commit:** `phase7(task-5): add secure compose profiles and topology`

## Task 6: Implement initialization, secrets, and enrollment

**Files**

- Create `scripts/compose-init.ps1` and fixture tests.
- Create configuration rendering and permission-check helpers.
- Create explicit admin-bootstrap, Agent-enroll, and Gateway-enroll scripts.
- Create generated-state manifest and validation logic.

**Behavior**

- Generate cryptographic secrets atomically with restrictive modes and no
  command-line secret arguments.
- Refuse existing unexpected files, weak permissions, symlinks, production
  insecure transport, mutable images, or reused environment IDs.
- Support development, single-host, and external-dependency initialization.
- Bootstrap is interactive where secrets are shown once.
- Enrollment tokens are one-time, never printed by default, and removed after
  successful durable session creation.
- Re-running init is idempotent and never rotates key material implicitly.

**Checks**

- Permission, entropy/length, collision, partial-write, interruption,
  idempotency, redaction, and generated-state Git exclusion tests.
- `docker compose config` and `docker inspect` canary tests prove secrets are
  not exposed.

**Task commit:** `phase7(task-6): add secure compose initialization and enrollment`

## Task 7: Add start, status, drain, and shutdown operations

**Files**

- Create `scripts/compose-up.ps1`, `compose-status.ps1`, and
  `compose-down.ps1`.
- Add dependency wait, migration job, readiness, snapshot, and smoke helpers.
- Add orphaned Runner detection/cleanup with explicit ownership checks.
- Add lifecycle operations documentation.

**Behavior**

- Validate host/release/configuration before starting containers.
- Start dependencies, migrate once, wait for Control Plane, enroll/connect
  Agent/Gateway, then require a fixed and pool readiness probe.
- Status reports bounded component/dependency/session/assignment categories
  without secrets.
- Shutdown drains Gateway, stops unsafe writes, waits for Agent/finalizers,
  removes only platform-owned Runners, and then stops dependencies.
- Timeout or orphan detection exits nonzero and preserves diagnostic state.

**Checks**

- Fresh start, repeated start, partial start, dependency delay, migration
  failure, SIGTERM, forced timeout, and orphan ownership tests.
- Prove unrelated host containers are never listed, stopped, or removed.

**Task commit:** `phase7(task-7): add compose lifecycle and graceful shutdown`

## Task 8: Add backup, restore, upgrade, and rollback

**Files**

- Create `scripts/compose-backup.ps1` and `compose-restore.ps1`.
- Create `scripts/compose-upgrade.ps1` and `compose-rollback.ps1`.
- Create backup/release manifest schemas and verification helpers.
- Add restore/upgrade operations documentation.

**Behavior**

- Back up PostgreSQL with schema/release/checksum metadata to an explicit
  destination and handle keyring material as a separate protected artifact.
- Exclude Redis, active leases, route caches, and ephemeral Runner state.
- Restore only to an empty/disposable stack after checksum, ownership,
  permission, schema, and keyring verification.
- Upgrade requires immutable target images, a verified pre-upgrade backup,
  migration compatibility, drain, readiness, convergence, and protocol smoke.
- Rollback validates the prior manifest and schema/data path; it never changes
  tags alone or mixes incompatible Control Plane schema versions.

**Checks**

- Backup canary/no-secret tests.
- Destructive teardown and restore with credential decryptability proof.
- Wrong key, corrupt dump, mismatched release, stale schema, partial upgrade,
  migration failure, and rollback recovery tests.
- Record measured single-host RPO/RTO exercise data.

**Task commit:** `phase7(task-8): add compose recovery and upgrade workflows`

## Task 9: Add Compose end-to-end, failure, and security gates

**Files**

- Create `tests/compose` harness and fake services.
- Add fixed/pool proxy smoke and isolation suites.
- Add restart/dependency-loss/security inspection suites.
- Add backup/restore and previous-manifest upgrade fixtures.

**Coverage**

- HTTP forwarding, HTTPS CONNECT, and SOCKS5 CONNECT through packaged Gateway,
  Agent relay, and fake Runner.
- Fixed endpoint and each pool strategy with current assignment/fence.
- Gateway/Agent/Control Plane/Runner restart and duplicate/reordered events.
- Redis loss/recovery, PostgreSQL interruption, node drain, account
  quarantine/replacement, and no capacity oversell.
- Cross-tenant credentials, routes, volumes, logs, diagnostics, and errors.
- Docker socket ownership, no privileged/host mode, secret non-exposure,
  read-only roots, capabilities, limits, and orphan cleanup.
- Backup/restore, upgrade, rollback, and bounded task/container counts.

**Checks**

- Run on clean Docker networks/volumes with deterministic teardown.
- Fake credentials and targets only; real-account suite remains protected.
- Repeat critical failure cases to catch flakes and leaked resources.

**Task commit:** `phase7(task-9): add compose end-to-end and security gates`

## Task 10: Add operations, CI, and release gates

**Files**

- Create `docs/operations/docker-compose-phase7.md`.
- Update `README.md` and compatibility matrix.
- Create `scripts/compose-ci.ps1` and its fixture tests.
- Create `.github/workflows/compose-ci.yml`.
- Add release-manifest, SBOM, vulnerability, and secret-scan gates.

**Behavior**

- Document prerequisites, modes, init, bootstrap, start, TLS/ingress,
  enrollment, status, scaling constraint, backup/restore, upgrade/rollback,
  Redis degradation, node drain, shutdown, troubleshooting, and data removal.
- State the Agent Docker-socket authority and the one-active-Gateway exact-limit
  constraint explicitly.
- CI builds both architectures, validates every Compose profile, starts the
  clean-host stack, runs E2E/failure/security/recovery tests, scans images, and
  proves deterministic cleanup.
- Update the future Helm mapping without adding Kubernetes resources.

**Checks**

```powershell
docker compose version
docker buildx version
docker compose -f deploy/compose/compose.yaml -f deploy/compose/compose.development.yaml config --quiet
docker compose -f deploy/compose/compose.yaml -f deploy/compose/compose.production.yaml config --quiet
go tool sqlc vet
go tool sqlc diff
go test -race -p 1 ./...
go vet ./...
go tool staticcheck ./...
cargo fmt --all --check
cargo clippy --workspace --all-targets --all-features -- -D warnings
cargo test --workspace --all-features
cargo deny check
powershell -NoProfile -File scripts/compose-ci.ps1
git diff --check
git status --short
```

**Task commit:** `phase7(task-10): add compose operations and release gates`

## Final verification gate

Run the Task 10 checks plus:

- clean-host development and single-host production installation;
- external PostgreSQL/Redis configuration validation;
- fixed and pool HTTP/CONNECT/SOCKS smoke tests;
- Redis and PostgreSQL interruption/recovery rehearsal;
- backup, destructive teardown, restore, and keyring verification;
- previous-manifest upgrade and rollback rehearsal;
- graceful shutdown and owned-Runner cleanup;
- `linux/amd64` and `linux/arm64` image build/inspection/SBOM/scan;
- inspection proving only Agent has Docker Engine access and no secret is
  exposed through Compose/container metadata.

Expected: all available gates exit zero, each of the ten tasks has a separate
commit, the Phase 7 worktree is clean, and the main worktree's unrelated
changes remain untouched before merge.
