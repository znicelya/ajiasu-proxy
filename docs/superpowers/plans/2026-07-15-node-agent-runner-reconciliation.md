# Phase 4 Node Agent, Runner Lifecycle, and Reconciliation Implementation Plan

**Goal:** Implement the versioned authenticated Node Agent protocol, platform node inventory, fixed endpoint lifecycle, idempotent isolated AJiaSu Runners, durable operations/finalizers, and restart-safe reconciliation.

**Architecture:** PostgreSQL stores all desired state, observations, operations, finalizers, and retry work. A Go gRPC control service delivers at-least-once commands to a Rust Agent. The Agent reconciles commands against Docker runtimes by stable Runner ID and generation. Credential plaintext is resolved only at delivery and injected into a private Runner tmpfs.

## Execution rules

- Use only fake AJiaSu credentials and the Phase 1 fake executable in ordinary CI.
- Never log protobuf bodies, credential configuration, Docker request bodies, runtime environment, container inspection JSON, or secret-provider values.
- Write failing tests before behavior changes and keep current/previous protocol fixtures immutable.
- Every tenant table uses forced RLS and every background task retains server-derived tenant scope.
- Persist operation/work identity before external side effects. External calls never occur while relying on process-memory-only state.
- Generated protobuf/sqlc files are not hand-edited.
- Do not add Gateway listeners, proxy credentials, traffic policy, pool scheduling, Redis, automatic migration/failover, Kubernetes orchestration, or Console code.

## Task 1: Freeze the Phase 4 contracts and compatibility policy

**Files:**

- Create: `docs/superpowers/specs/2026-07-15-node-agent-runner-reconciliation-design.md`
- Create: `docs/superpowers/plans/2026-07-15-node-agent-runner-reconciliation.md`
- Create: `docs/adr/0002-agent-runtime-boundary.md`
- Create: `api/proto/buf.yaml`
- Create: `api/proto/buf.gen.yaml`
- Create: `api/proto/agent/v1/agent.proto`
- Create: `api/proto/agent/v1/testdata/revision-1.json`
- Create: `api/proto/agent/v1/testdata/revision-2.json`

- [ ] Define node enrollment/session, stream envelope, command, observation, inventory, heartbeat, and result messages.
- [ ] Reserve field numbers and stable enums; prohibit reuse and secret-bearing diagnostic strings.
- [ ] Define current revision 2, previous revision 1, negotiation, downgrade, and rejection behavior.
- [ ] Define the Agent/container-runtime privilege boundary and Docker socket ownership.
- [ ] Add Buf lint and breaking-change tests against the checked-in previous descriptor.

## Task 2: Add the Rust workspace, generated protocol bindings, and Agent build gate

**Files:**

- Create: `Cargo.toml`
- Create: `Cargo.lock`
- Create: `rust-toolchain.toml`
- Create: `deny.toml`
- Create: `crates/agent-protocol/Cargo.toml`
- Create: `crates/agent-protocol/build.rs`
- Create: `crates/agent-protocol/src/lib.rs`
- Create: `crates/agent/Cargo.toml`
- Create: `crates/agent/src/main.rs`
- Create: `crates/agent/src/config.rs`
- Create: `internal/gen/agent/v1/`
- Create: `scripts/agent-ci.ps1`
- Create: `.github/workflows/agent-ci.yml`
- Modify: `.gitignore`

- [ ] Pin the reviewed Rust toolchain and dependency lock.
- [ ] Generate Go and Rust bindings from the same protobuf source.
- [ ] Fail CI when generated bindings or descriptors differ.
- [ ] Enable `cargo fmt --check`, clippy with warnings denied, tests, cargo-deny, and vulnerability scanning.
- [ ] Cross-compile Agent binaries for `linux/amd64` and `linux/arm64`.

## Task 3: Add schema version 9 for nodes, endpoints, operations, and reconciliation

**Files:**

- Create: `migrations/00009_nodes_endpoints_operations.sql`
- Create: `sql/queries/nodes.sql`
- Create: `sql/queries/endpoints.sql`
- Create: `sql/queries/operations.sql`
- Create: `sql/queries/reconciler.sql`
- Modify: `sqlc.yaml`
- Create: `internal/nodes/dbgen/`
- Create: `internal/endpoints/dbgen/`
- Create: `internal/operations/dbgen/`
- Create: `internal/reconciler/dbgen/`
- Modify: `cmd/control-plane/runtime.go`

- [ ] Create node, enrollment, and session tables with one-time/expiry/session-generation constraints.
- [ ] Create fixed endpoint spec/status tables with forced RLS and optimistic versions.
- [ ] Create Runner desired/observed state, operations, work leases, and finalizers.
- [ ] Add uniqueness constraints for one logical work item per resource/action/generation and one active Runner per endpoint.
- [ ] Ensure all tenant foreign keys include tenant identity and cannot cross scope.
- [ ] Implement down migration and advance supported schema version from 8 to 9.

## Task 4: Implement node enrollment, sessions, inventory, and management APIs

**Files:**

- Create: `internal/nodes/model.go`
- Create: `internal/nodes/service.go`
- Create: `internal/nodes/enrollment.go`
- Create: `internal/nodes/session.go`
- Create: `internal/nodes/http.go`
- Create: `internal/nodes/service_test.go`
- Create: `internal/nodes/http_test.go`
- Create: `internal/nodes/isolation_test.go`
- Modify: `internal/tenancy/policy.go`
- Modify: `api/openapi/control-plane.yaml`

- [ ] Return enrollment and session plaintext tokens once and store only slow verifiers.
- [ ] Consume enrollment atomically with node creation/registration and audit rejection categories safely.
- [ ] Rotate short-lived sessions and bind them to node, Agent instance, generation, and protocol revision.
- [ ] Implement node list/get/update/drain with active/cordoned/draining/disabled transitions.
- [ ] Implement tenant-safe eligible-node projection without host/runtime diagnostics.
- [ ] Prove tenant roles cannot invoke platform node mutations and Agent credentials cannot call HTTP management routes.

## Task 5: Implement the Go gRPC control service and durable command delivery

**Files:**

- Create: `internal/nodes/grpc_server.go`
- Create: `internal/nodes/grpc_auth.go`
- Create: `internal/nodes/stream_registry.go`
- Create: `internal/nodes/heartbeat.go`
- Create: `internal/nodes/grpc_test.go`
- Create: `internal/nodes/protocol_compatibility_test.go`
- Modify: `internal/platform/config/config.go`
- Modify: `cmd/control-plane/runtime.go`
- Modify: `cmd/control-plane/main.go`

- [ ] Add a TLS gRPC listener with explicit loopback-only insecure development mode.
- [ ] Negotiate revisions 2/1 and reject unsupported, inconsistent, expired, revoked, or cross-node sessions.
- [ ] Authenticate every stream message to the bound node and cap message sizes/rates.
- [ ] Persist commands before delivery and use operation ID/generation for acknowledgement and retry.
- [ ] Request full inventory after connect/reconnect before allowing create delivery.
- [ ] Add graceful shutdown that releases stream ownership without losing durable work.

## Task 6: Implement the Rust Agent and idempotent runtime adapters

**Files:**

- Create: `crates/agent/src/client.rs`
- Create: `crates/agent/src/session.rs`
- Create: `crates/agent/src/inventory.rs`
- Create: `crates/agent/src/commands.rs`
- Create: `crates/agent/src/secret.rs`
- Create: `crates/agent/src/runtime/mod.rs`
- Create: `crates/agent/src/runtime/process.rs`
- Create: `crates/agent/src/runtime/docker.rs`
- Create: `crates/agent/tests/idempotency.rs`
- Create: `crates/agent/tests/restart_inventory.rs`
- Create: `Dockerfile.agent`
- Create: `build/agent-images.lock`
- Create: `scripts/lock-agent-images.ps1`

- [ ] Implement reconnect, revision fallback, heartbeat, inventory snapshot, acknowledgements, and operation results.
- [ ] Implement runtime trait semantics for create/stop/rebuild/garbage-collect and stale-generation rejection.
- [ ] Make duplicate create produce one runtime and duplicate stop of an absent runtime succeed.
- [ ] Inventory Docker runtimes by exact ownership/node labels after Agent restart.
- [ ] Use Docker Engine API, never Docker CLI, and manage only exact platform-owned runtimes.
- [ ] Build a non-root minimal Agent image for both supported architectures.

## Task 7: Enforce Runner isolation and restricted credential injection

**Files:**

- Modify: `crates/agent/src/runtime/docker.rs`
- Modify: `crates/agent/src/secret.rs`
- Modify: `runner/bin/runner-entrypoint.sh`
- Create: `crates/agent/tests/docker_isolation.rs`
- Create: `tests/security/phase4_secret_injection_test.go`
- Create: `tests/isolation/phase4_runner_isolation_test.go`
- Modify: `Dockerfile`

- [ ] Create one non-host network namespace, tmpfs configuration directory, and cache directory per Runner.
- [ ] Enforce non-root, read-only root, `no-new-privileges`, dropped capabilities, resource limits, and immutable Runner image digest.
- [ ] Keep credential bytes out of environment, labels, arguments, container metadata, persistent cache, and host paths.
- [ ] Set configuration mode `0400`, zero Agent/control-plane buffers, and remove tmpfs with Runner cleanup.
- [ ] Prove two tenant Runners cannot read each other's files, cache, process, or network namespace.
- [ ] Scan Agent/control-plane logs, gRPC errors, Docker metadata, audit, Outbox, traces, metrics, and disk for credential canaries.

## Task 8: Implement fixed endpoints, operations, finalizers, and reconciliation

**Files:**

- Create: `internal/endpoints/model.go`
- Create: `internal/endpoints/service.go`
- Create: `internal/endpoints/http.go`
- Create: `internal/endpoints/service_test.go`
- Create: `internal/endpoints/http_test.go`
- Create: `internal/operations/model.go`
- Create: `internal/operations/service.go`
- Create: `internal/operations/http.go`
- Create: `internal/reconciler/worker.go`
- Create: `internal/reconciler/endpoint.go`
- Create: `internal/reconciler/delivery.go`
- Create: `internal/reconciler/finalizer.go`
- Create: `internal/reconciler/retry.go`
- Create: `internal/reconciler/reconciler_test.go`
- Modify: `internal/accounts/service.go`
- Modify: `cmd/control-plane/runtime.go`
- Modify: `api/openapi/control-plane.yaml`

- [ ] Accept only fixed account/fixed node endpoints and return stable future-feature errors for pool binding.
- [ ] Return `202 Accepted` operations for create/start/stop/rebuild/delete actions.
- [ ] Reserve account capacity before create delivery and renew it while the Runner may exist.
- [ ] Reject cordoned/draining/disabled/offline nodes and node capacity exhaustion.
- [ ] Apply observations only when Runner ID and generation match desired state.
- [ ] Keep `runner.cleanup` until confirmed stop/absence, then release capacity exactly once and finalize deletion.
- [ ] Resume expired work leases after control-plane restart without duplicating logical operations.
- [ ] Retain reservations on stale/offline nodes and require audited platform-admin force-finalization.

## Task 9: Add restart, failure, compatibility, migration, and operations gates

**Files:**

- Create: `tests/contract/phase4_agent_protocol_test.go`
- Create: `tests/integration/phase4_test.go`
- Create: `tests/isolation/phase4_isolation_test.go`
- Create: `tests/failure/phase4_reconciliation_test.go`
- Create: `docs/operations/control-plane-phase4.md`
- Modify: `internal/platform/httpserver/openapi_test.go`
- Modify: `scripts/control-plane-ci.ps1`
- Modify: `.github/workflows/control-plane-ci.yml`
- Modify: `scripts/agent-ci.ps1`

- [ ] Exercise enrollment, registration, heartbeat, fixed endpoint create, running observation, rebuild, stop, delete, and operation reads.
- [ ] Inject duplicate, delayed, and out-of-order commands/reports and prove deterministic convergence.
- [ ] Restart Agent and Docker and prove inventory reconstruction without duplicate Runner creation.
- [ ] Restart the control plane during every operation phase and prove work/finalizer recovery.
- [ ] Prove node loss retains capacity and cannot trigger unsafe duplicate login.
- [ ] Rehearse migration `8 -> 9 -> 8 -> 9` and current/previous application rolling compatibility.
- [ ] Document enrollment, TLS, session rotation, node maintenance, Docker socket boundary, force-finalization, recovery, and Phase 4 exclusions.
- [ ] Run protobuf, Go, Rust, secret, isolation, SBOM, vulnerability, and multi-architecture image gates in CI.

## Final verification

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
powershell -NoProfile -File scripts/agent-ci.ps1
powershell -NoProfile -File scripts/control-plane-ci.ps1
docker buildx build --no-cache --pull=false --platform linux/amd64,linux/arm64 --file Dockerfile.agent --output type=cacheonly .
git diff --check
git status --short
```

Expected: all commands exit zero. Work remains uncommitted until explicitly requested.
