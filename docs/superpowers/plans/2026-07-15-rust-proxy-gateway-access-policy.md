# Phase 5 Rust Proxy Gateway and Access Policy Implementation Plan

**Goal:** Implement the first data-plane surface: endpoint proxy credentials, typed access policy, a Rust Gateway for HTTP forward proxy/HTTPS CONNECT/SOCKS5 TCP CONNECT, and a signed Gateway-to-Agent-to-Runner relay.

**Entry dependency:** Phase 4 is merged. Migration 9, endpoint identity, Runner ID/generation, Agent sessions, observations, operations, and finalizers are stable.

**Scope:** Fixed endpoints only. Pool scheduling, Redis fencing, account failover, node migration, quarantine automation, and global multi-Gateway allocation remain Phase 6.

## Mandatory worktree and commit rule

Create a dedicated worktree before implementation:

~~~powershell
git worktree add .worktrees/phase-5-gateway-access -b feat/phase-5-gateway-access main
~~~

Every task below is independently reviewable. A task is incomplete until its tests pass, its worktree is clean, and its changes are committed. Never start the next task with a dirty worktree.

At the end of **every** task, run:

~~~powershell
git diff --check
git status --short
git add <task files>
git diff --cached --check
git commit -m "phase5(task-N): <short outcome>"
git status --short
~~~

The final status must be empty. Do not merge the Phase 5 branch until every task and the final gate pass. Follow-up fixes use a new commit; do not leave changes uncommitted.

## Execution rules

- Write a failing test before each behavior change.
- Use fake AJiaSu credentials and the Phase 1 fake Runner in ordinary CI.
- Never log proxy passwords, Proxy-Authorization, SOCKS credentials, request/response bodies, target URLs, DNS answers, route tickets, socket paths, encrypted AJiaSu fields, or arbitrary upstream errors.
- Generated protobuf and sqlc files are regenerated, never hand-edited.
- Every tenant query uses server-derived tenant scope and forced RLS.
- Gateway has no Docker socket, PostgreSQL credentials, or AJiaSu credential-provider access.
- Phase 5 uses one active Gateway for exact aggregate counters; do not add Redis, fencing, pool scheduling, or automatic failover.

## Task 1: Freeze Gateway and relay contracts

**Files**

- Create gateway and relay protobufs under api/proto/gateway/v1 and api/proto/relay/v1.
- Add immutable current/previous JSON fixtures.
- Create docs/adr/0003-gateway-relay-boundary.md.
- Update api/proto/buf.yaml and buf.gen.yaml.

**Behavior**

- Define Gateway enrollment/session, route snapshots/deltas, route-grant refresh, heartbeat, snapshot acknowledgement, and bounded health messages.
- Define relay metadata/data/half-close/error frames and reserve field numbers.
- Define mTLS Gateway identity, Ed25519 route grants, Agent relay authorization, Runner-local Unix socket ownership, and prohibited payload logging.
- Reject unsupported or stale protocol revisions before data delivery.

**Checks**

- Contract fixture tests.
- Buf lint/breaking checks when buf is installed.
- Deterministic descriptor and generated-binding checks.

**Task commit:** phase5(task-1): freeze gateway and relay contracts

## Task 2: Add schema 10

**Files**

- Create migration 00010_gateway_access_policy.sql.
- Add sql/queries/proxy_credentials.sql, access_profiles.sql, gateways.sql, and gateway_usage.sql.
- Extend sqlc.yaml and generate internal/proxyaccess/dbgen and internal/gateways/dbgen.
- Update runtime schema readiness.

**Behavior**

- Tenant proxy credentials store public ID, Argon2id verifier, expiry, revocation, and version metadata only.
- Tenant access profiles store canonical policy hash/version under forced RLS.
- Platform Gateway enrollment/session/health tables contain no tenant secrets.
- Route-grant metadata and bounded traffic-usage windows are durable without storing route tickets or passwords in plaintext.
- Down migration and readiness rehearsal support 9 -> 10 -> 9 -> 10.

**Checks**

- Migration, restart, RLS, and cross-tenant lookup tests.
- sqlc generate, vet, and diff.

**Task commit:** phase5(task-2): add gateway access schema and sqlc bindings

## Task 3: Add canonical policy evaluation

**Files**

- Create crates/proxy-policy with CIDR, domain, port, DNS, limit, and evaluator modules.
- Add internal/proxyaccess/policy.go and golden-vector tests.

**Behavior**

- Canonicalize typed JSON into deterministic policy documents and hashes.
- Validate protocol sets, CIDRs, IDNA domains, port ranges, DNS mode, and bounded connection/rate/idle/byte limits.
- Apply fixed precedence: source intersection, target parsing, platform safety deny, explicit deny, explicit allowlist, then resource limits.
- Check every local or Runner-side DNS answer, including all A/AAAA results.
- Reject wildcard domains, contradictory rules, unsafe numeric targets, and attempts to remove platform denies.

**Checks**

- Rust property tests and Go/Rust golden vectors.
- Loopback, link-local, private, metadata, management, IPv6, IDNA, DNS-rebinding, and port-boundary cases.

**Task commit:** phase5(task-3): add canonical proxy policy evaluation

## Task 4: Implement proxy credential and access APIs

**Files**

- Create internal/proxyaccess model/service/http and tests.
- Modify tenancy policy, OpenAPI, and runtime wiring.

**Behavior**

- Create/rotate returns generated proxy username/password exactly once.
- Store only Argon2id verifier and safe metadata; idempotency never stores the request body.
- Revoke/expire credentials and publish durable route-delta events.
- Create/update access profiles with typed validation, optimistic versions, stable errors, and pool-binding rejection.
- Audit credential issue/rotate/revoke and policy changes without secrets or raw policy JSON.

**Checks**

- Correct/wrong/expired/revoked verification.
- One-time response and retry tests.
- Cross-tenant and role-matrix tests.
- OpenAPI/registered-route consistency.

**Task commit:** phase5(task-4): implement proxy credentials and access APIs

## Task 5: Implement Gateway enrollment and snapshots

**Files**

- Create internal/gateways model/service/grpc_server/stream_registry/snapshot and tests.
- Modify config and control-plane runtime.

**Behavior**

- One-time Gateway enrollment, mTLS identity binding, short-lived sessions, revocation, and graceful stream replacement.
- Build complete snapshots from active fixed endpoints, profiles, verifiers, running Runner observations, and valid grants.
- Send a full snapshot after registration/reconnect, then ordered monotonic deltas.
- Sign grants with a dedicated Ed25519 key bound to Gateway, endpoint, Runner, generation, protocol, policy hash, and expiry.
- Never include AJiaSu fields, Docker identifiers, raw bodies, or arbitrary Agent diagnostics.

**Checks**

- Enrollment/expiry/revocation and mTLS tests.
- Snapshot filtering and grant signature/audience/generation/expiry tests.
- Reconnect and stale-delta ordering tests.

**Task commit:** phase5(task-5): add gateway enrollment and route snapshots

## Task 6: Build bounded Rust protocol parsers

**Files**

- Create crates/proxy-protocol for HTTP forward, CONNECT, SOCKS5, and bounds.
- Add malformed and compatibility/property tests.

**Behavior**

- Parse HTTP/1.1 absolute-form, CONNECT authority-form, and RFC1928/1929 SOCKS5 TCP CONNECT only.
- Enforce header, target, authentication, frame, handshake, and allocation bounds before expensive work.
- Implement half-close-safe copying, idle/deadline handling, and stable protocol responses.
- Remove proxy hop-by-hop headers and never forward Proxy-Authorization.
- Reject NO AUTH, GSSAPI, UDP ASSOCIATE, BIND, unsupported HTTP methods, malformed input, and slow clients.

**Task commit:** phase5(task-6): add bounded proxy protocol parsers

## Task 7: Add Agent relay and Runner-local boundary

**Files**

- Add crates/agent relay and route-grant modules.
- Add crates/runner-relay.
- Extend relay protobuf.
- Modify Docker runtime, runner entrypoint, and Dockerfile.
- Add relay security/isolation tests.

**Behavior**

- Agent exposes an mTLS Gateway relay with bounded concurrent streams.
- Verify grant signature, Gateway audience, endpoint/Runner/generation, policy hash, protocol, and expiry before opening a Runner socket.
- Mount only a per-Runner relay Unix socket; keep credentials in a separate 0400 tmpfs file.
- Runner relay resolves Runner-mode DNS inside the Runner namespace and rechecks immutable safety denies.
- Support metadata once, bounded data frames, half-close, idle timeout, and no body logging.

**Checks**

- Invalid signature/audience/expiry/generation/policy tests.
- Cross-Runner socket isolation and stale-socket cleanup.
- Credential-canary scan across relay paths.

**Task commit:** phase5(task-7): add signed Agent to Runner relay

## Task 8: Implement Gateway control client and routing

**Files**

- Create crates/gateway with config, control, routes, auth, relay, limits, and main.
- Create Dockerfile.gateway.

**Behavior**

- Register with control plane, apply snapshots atomically, reject out-of-order deltas, and expire stale grants.
- Expose separate HTTP/CONNECT and SOCKS5 listeners with explicit bounds and mTLS/control configuration.
- Authenticate proxy credentials with bounded Argon2id work and source failure buckets.
- Select a fixed endpoint only when endpoint, Runner generation, protocol, policy version, and grant are current.
- Open Agent relay streams and account every close/error path.
- Gateway has no Docker, PostgreSQL, or AJiaSu credential dependency.

**Checks**

- Snapshot/reconnect/expiry tests.
- HTTP/CONNECT/SOCKS auth and route-selection integration tests.
- Argon2 semaphore and auth-rate exhaustion tests.

**Task commit:** phase5(task-8): implement Gateway control and data routing

## Task 9: Enforce resource and traffic limits

**Files**

- Modify crates/gateway limits.
- Add internal/gateways usage service/tests.
- Update usage SQL and OpenAPI if quota status is exposed.

**Behavior**

- Enforce endpoint/credential concurrent permits and return them exactly once.
- Enforce token-bucket connection rates, idle deadlines, per-connection bytes, and traffic-window budgets.
- Batch usage deltas to durable PostgreSQL windows with bounded overshoot.
- Return stable quota-exhausted errors and keep metrics free of domains, IPs, usernames, credential IDs, and high-cardinality tenant labels.

**Checks**

- Concurrent permit/property tests.
- Rate/byte/idle boundary tests.
- Restart and usage-window reconciliation.
- Forced close, timeout, half-close, and shutdown accounting.

**Task commit:** phase5(task-9): enforce Gateway resource and traffic limits

## Task 10: Add integration, isolation, failure, and operations gates

**Files**

- Create tests/integration/phase5_gateway_test.go.
- Create tests/contract/phase5_proxy_protocol_test.go.
- Create tests/security/phase5_proxy_secret_log_test.go.
- Create tests/isolation/phase5_gateway_isolation_test.go.
- Create tests/failure/phase5_gateway_failure_test.go.
- Create docs/operations/gateway-phase5.md.
- Add scripts/gateway-ci.ps1 and gateway workflow; update control-plane CI.

**Coverage**

- Enrollment -> snapshot -> proxy credential -> fixed endpoint -> Agent relay -> Runner -> target for HTTP, CONNECT, and SOCKS5.
- Duplicate/reordered snapshots, revoked credentials, stale generations, Runner stop, Agent loss, Gateway restart, control-plane restart, and expired grants.
- Cross-tenant IDs, credentials, policies, snapshots, relay sockets, logs, traces, metrics, and errors.
- SSRF/metadata/private-range, DNS rebinding, slowloris, oversized headers, unsupported SOCKS methods, and half-close.
- Enrollment, TLS rotation, route-cache recovery, quota operations, and the single-Gateway exact-limit constraint.

**Task commit:** phase5(task-10): add Phase 5 integration and operations gates

## Final verification gate

Run from the Phase 5 worktree after Task 10 is committed:

~~~powershell
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
powershell -NoProfile -File scripts/gateway-ci.ps1
powershell -NoProfile -File scripts/control-plane-ci.ps1
docker buildx build --no-cache --pull=false --platform linux/amd64,linux/arm64 --file Dockerfile.gateway --output type=cacheonly .
docker buildx build --no-cache --pull=false --platform linux/amd64,linux/arm64 --file Dockerfile --output type=cacheonly .
git diff --check
git status --short
~~~

Expected: all commands exit zero, every task has a separate commit, and the worktree is clean. Only then may the branch be reviewed and merged into main.

