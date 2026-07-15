# Phase 4 Node Agent, Runner Lifecycle, and Reconciliation Design

## 1. Goal

Phase 4 introduces the authenticated Node Agent control channel, platform node inventory, idempotent AJiaSu Runner lifecycle, desired/observed state, long-running operations, finalizers, and restart-safe reconciliation.

It consumes the Phase 3 account identity, credential-version, secret-provider, and capacity-reservation contracts. It establishes the stable Runner routing/status contract needed by the Phase 5 Gateway without opening a proxy listener or exposing a Runner address to management clients.

## 2. Scope boundaries

Phase 4 includes:

- A Rust Node Agent and a version-negotiated authenticated gRPC protocol.
- One-time node enrollment, short-lived Agent sessions, heartbeat, inventory, labels, capacity, and maintenance state.
- A minimal tenant endpoint lifecycle used only to request a fixed account on a fixed node.
- Idempotent Runner create, stop, rebuild, inventory, and garbage-collect commands.
- PostgreSQL-backed desired state, observed state, operations, retry work, and finalizers.
- Just-in-time credential resolution and restricted Runner credential injection.
- Docker-based Runner isolation plus a deterministic process runtime used only by tests.

Phase 4 explicitly excludes:

- HTTP proxy, HTTPS CONNECT, SOCKS5, Gateway routing, proxy credentials, and traffic policy.
- Pool scheduling, least-connections allocation, Redis leases, fencing tokens, and account-pool failover.
- Automatic migration to another node, account quarantine, or tunnel/egress health automation.
- Kubernetes Runner orchestration, Helm, Compose production packaging, and Web Console work.
- Any management API that returns Runner internal addresses, runtime identifiers, files, or plaintext AJiaSu credentials.

The minimal endpoint resource accepts only `binding_mode=fixed`, an explicit account ID, and an explicit node ID. Pool binding remains a documented but rejected future value until Phase 6.

## 3. Confirmed architectural decisions

- PostgreSQL remains authoritative. No command, retry, desired state, operation, or finalizer exists only in process memory.
- The control plane owns desired state; Agents own observations. Agent reports cannot mutate endpoint specifications.
- API writes return after desired state and an `Operation` commit. They do not wait for Runner startup.
- Reconciliation and command delivery are at-least-once. Correctness comes from stable operation IDs, Runner IDs, generations, and idempotent Agent behavior.
- Phase 4 uses PostgreSQL work leasing with `FOR UPDATE SKIP LOCKED`. Redis is not introduced.
- Every Runner has exactly one tenant, endpoint, account, node, credential version, network namespace, cache directory, and runtime identity.
- The Agent is the only component allowed to access the local container runtime. Control Plane, Gateway, and Console never receive the Docker socket.
- The first production runtime adapter uses the Docker Engine API over a local Unix socket. Shelling out to the Docker CLI is prohibited.
- A test-only process runtime runs fake AJiaSu without container privileges and implements the same lifecycle trait.
- Account capacity is reserved before a create command is eligible for delivery. It is released only after confirmed cleanup or an audited platform-admin force-finalization.
- Node loss does not automatically release account capacity or create a duplicate Runner. Phase 6 adds safe failover and fencing.
- Credential plaintext is resolved only immediately before command delivery, exists only in control-plane and Agent memory plus the target Runner's private tmpfs, and is never stored in operations, Outbox, retry rows, observations, or Agent journals.
- Schema version advances from 8 to 9.

## 4. gRPC protocol and compatibility

### 4.1 Transport

The control plane exposes a dedicated TLS gRPC listener. Plaintext transport is allowed only for an explicitly enabled loopback development profile. Production startup fails when certificate/key configuration is absent or filesystem permissions are unsafe.

The protocol lives under `api/proto/agent/v1`. The API package version is stable while a negotiated `protocol_revision` permits additive evolution. Phase 4 defines current revision `2` and previous revision `1` fixtures. The server and Agent support both; the Agent prefers revision 2 and falls back to revision 1. Unsupported or downgrade-inconsistent revisions fail before commands are accepted.

Buf lint, descriptor generation, and breaking-change checks protect the current and previous revision window. Additive optional fields may be ignored by revision-1 peers. Field numbers and enum values are never reused.

### 4.2 Enrollment and session authentication

Platform administrators create one-time node enrollments through the management API. An enrollment:

- Is stored as a slow verifier, never plaintext.
- Expires after 15 minutes by default and can be revoked before use.
- Is bound to an expected node name and optional bootstrap constraints.
- Is consumed exactly once in the registration transaction.

`RegisterNode` exchanges the enrollment token for a node ID and an opaque Agent session token. Agent session tokens:

- Are stored only as slow verifiers.
- Expire after 24 hours by default.
- Are bound to the node, Agent instance, and negotiated protocol revision.
- Rotate before the final quarter of their lifetime.
- Are invalidated when a node is disabled or its session generation changes.

Agent identities are not platform administrators and cannot call management HTTP APIs.

### 4.3 RPC shape

The protocol exposes:

```text
RegisterNode(RegisterNodeRequest) returns (RegisterNodeResponse)
Connect(stream AgentMessage) returns (stream ControlMessage)
```

The Agent stream sends:

- `hello`: node ID, Agent instance ID, selected revision, Agent version, architecture, and runtime capabilities.
- `heartbeat`: observed labels, capacity, active Runner count, maintenance acknowledgement, and timestamp.
- `inventory_snapshot`: all managed Runners discovered from the local runtime.
- `runner_observation`: Runner state and observed generation.
- `operation_result`: operation ID, stable result code, and safe bounded message.
- `command_ack`: operation ID and acceptance/rejection code.

The control stream sends:

- `session_renewal`.
- `runner_command` with action `create`, `stop`, `rebuild`, or `garbage_collect`.
- `desired_inventory_request` after registration, reconnect, or reconciliation uncertainty.
- `maintenance_command` for cordon/drain acknowledgement.

Every Runner command carries operation ID, Runner ID, tenant ID, endpoint ID, account ID, credential version, desired generation, command deadline, and an immutable runtime specification. Create/rebuild delivery may additionally carry credential configuration bytes resolved just in time. Those bytes are not part of persisted command payloads and are never included in message logging.

## 5. Data model

Migration `00009_nodes_endpoints_operations.sql` creates `nodes`, `endpoints`, `operations`, and `reconciler` schemas.

### 5.1 Platform nodes

`nodes.nodes` is platform-scoped:

- UUIDv7 ID, unique normalized name, desired labels, and observed labels.
- Desired maximum Runners and reserved headroom.
- Observed active Runners, architecture, Agent version, and runtime capabilities.
- Maintenance state: `active`, `cordoned`, `draining`, or `disabled`.
- Connectivity state: `registering`, `online`, `stale`, or `offline`.
- Last heartbeat, session generation, resource version, and timestamps.

`nodes.node_enrollments` and `nodes.node_sessions` store token verifiers, expiry, consumption/revocation state, and safe actor metadata. Raw tokens are returned once.

Nodes are shared platform capacity, not tenant-owned resources. Tenant APIs expose only an eligible-node projection: ID, display name, approved labels, maintenance/connectivity state, and coarse capacity. They never expose Agent addresses, runtime socket paths, container IDs, or host diagnostics.

### 5.2 Minimal endpoints

`endpoints.proxy_endpoints` is tenant-scoped with forced RLS:

- UUIDv7 ID, tenant ID, unique normalized name, and resource version.
- `binding_mode`, constrained to `fixed` in Phase 4.
- Explicit account ID and explicit node ID.
- Desired Runner state: `running` or `stopped`.
- Lifecycle: `active`, `disabled`, or `deleting`.
- Monotonic desired generation and timestamps.

`endpoints.endpoint_status` is tenant-scoped observed state:

- Observed generation and state: `pending`, `starting`, `running`, `stopping`, `stopped`, `failed`, or `orphaned`.
- Runner ID, safe reason code, last transition, and last Agent observation.
- No internal address, container ID, config path, or secret-bearing message.

Protocol exposure, proxy credentials, destination policy, DNS mode, and Gateway bindings are added in Phase 5 without changing endpoint identity or lifecycle semantics.

### 5.3 Runner desired and observed state

`reconciler.runner_desired_states` records:

- Tenant, endpoint, Runner, node, account, and credential version.
- Desired generation and desired action.
- Stable operation ID and immutable non-secret runtime specification.
- Capacity reservation ID and finalizer state.

`reconciler.runner_observations` records the most recent Agent report:

- Node, tenant, endpoint, Runner, operation, and observed generation.
- Runtime state, process exit category, restart count, and last-seen time.
- A bounded stable reason code and whitelisted diagnostic counters.

Runtime identifiers may be stored for internal reconciliation but are never returned through tenant APIs or written into audit detail.

### 5.4 Operations, work, and finalizers

`operations.operations` represents tenant or platform long-running work:

- Type, resource identity, requested generation, state, attempts, progress category, safe result code, and timestamps.
- States: `queued`, `running`, `succeeded`, `failed`, or `cancelled`.
- Messages are bounded, selected from safe templates, and cannot contain Agent/backend text.

`reconciler.work_items` is the durable retry queue. A unique resource/action/generation key prevents duplicate logical work. Workers lease rows with PostgreSQL timestamps and `SKIP LOCKED`; expired leases are recoverable after process restart.

`reconciler.finalizers` contains `runner.cleanup` for endpoints with possible data-plane state. An endpoint in `deleting` remains readable until the Agent confirms Runner removal, the capacity reservation is released, and the finalizer is removed.

All tenant-bearing tables enable and force RLS. Platform node tables are accessible only to the platform database role and dedicated internal repositories.

## 6. Runner lifecycle and Agent idempotency

The Agent runtime trait supports:

```text
Inventory
Create
Stop
Rebuild
GarbageCollect
```

Idempotency rules:

- `Create` for an existing Runner with the same generation and runtime specification returns the existing Runner.
- `Create` for an existing older generation converges through rebuild; it never creates a second Runner with the same Runner ID.
- `Stop` for an absent Runner succeeds.
- `Rebuild` stops the prior runtime before creating the replacement and preserves the stable Runner ID.
- `GarbageCollect` affects only runtimes carrying the exact platform ownership label and local node ID, and only after an explicit control-plane command plus orphan grace period.
- Duplicate or out-of-order commands compare desired generation. Older generations are acknowledged as stale and cannot overwrite newer state.

Docker Runners use:

- One container and network namespace per Runner; host networking is forbidden.
- The reviewed Phase 1 Runner image by immutable digest.
- Non-root UID 65532, read-only root filesystem, `no-new-privileges`, dropped capabilities, resource limits, and only explicitly justified devices/capabilities.
- A private tmpfs for `/run/ajiasu` and a Runner-specific cache directory with restrictive ownership.
- Runtime labels for platform ownership, node ID, Runner ID, tenant ID, endpoint ID, operation ID, and desired generation.
- No host path containing plaintext credentials and no credential in environment variables, labels, command arguments, or container metadata.

On Agent restart, the Agent inventories owned runtimes from the Docker API, reconstructs local state from immutable labels and runtime inspection, opens a new stream, and sends a full snapshot before accepting new create commands.

## 7. Credential delivery

The persisted desired state contains only account ID and credential version. Immediately before create/rebuild delivery, the control plane:

1. Confirms the endpoint generation, operation lease, Agent session, node state, account lifecycle, credential version, and capacity reservation.
2. Opens the credential through the Phase 3 `secrets.Provider` using tenant/account/version authenticated context.
3. Encodes the AJiaSu configuration in a dedicated non-logging type.
4. Sends it only on the authenticated TLS stream.
5. Zeroes control-plane buffers after the send completes or fails.

The Agent wraps received secret bytes in zeroizing memory, writes the configuration into the target Runner's private tmpfs with mode `0400`, starts the Runner, and zeroes its buffers. The file exists only for that Runner lifetime and is removed with the container. It is never copied into the persistent cache directory.

gRPC interceptors log method, request ID, node ID, operation ID, duration, and stable status only. They never log or format protobuf request/response bodies.

## 8. Reconciliation

Endpoint reconciliation follows this sequence:

1. Lock the endpoint and current desired state.
2. Ignore stale work whose generation no longer matches.
3. For desired `running`, validate fixed account/node eligibility, reserve Phase 3 account capacity, create a stable Runner ID and operation, and persist the create work atomically.
4. Deliver the command only to the authenticated session for the selected node.
5. Apply Agent observations only to the matching Runner and generation.
6. Mark the operation succeeded when the observed generation is running.
7. For stopped/deleting endpoints, persist stop work and retain the cleanup finalizer.
8. After confirmed absence/stopped state, release capacity, clear desired/observed Runner state, remove the finalizer, and finalize deletion.

Default retry delay is exponential from one second to five minutes with jitter. Command deadlines and retry ceilings are action-specific. A failed operation remains inspectable; reconciliation may create a new operation only for a new generation or explicit rebuild request.

Control-plane restart reconstructs runnable work from `work_items`, desired state, operations, and current observations. Agent reconnect reconstructs its side from runtime inventory. Neither side assumes a command was lost or completed solely because a connection closed.

## 9. Node maintenance and failure behavior

- `active`: accepts explicitly bound Runner creation when capacity permits.
- `cordoned`: keeps existing Runners but rejects new create/rebuild placement.
- `draining`: rejects new work and requests bounded stop of existing Phase 4 Runners. Endpoints remain pending/stopped; automatic reassignment is deferred to Phase 6.
- `disabled`: invalidates Agent sessions and rejects all commands.

Heartbeats default to 10 seconds. A node becomes stale after 45 seconds and offline after five minutes, both configurable within safe bounds. Stale/offline transitions do not release account reservations or create replacement Runners. This prevents concurrency oversell when the old Runner may still exist.

A platform administrator may force-finalize an unreachable Runner only through a separately authorized, idempotent action that records the node, Runner, endpoint, reservation, reason category, and acknowledgement of possible duplicate-login risk. This is an emergency recovery path, not automatic reconciliation.

## 10. Management API and authorization

Platform routes:

```text
POST       /api/v1/node-enrollments
DELETE     /api/v1/node-enrollments/{enrollment_id}
GET        /api/v1/nodes
GET/PATCH  /api/v1/nodes/{node_id}
POST       /api/v1/nodes/{node_id}/drain
GET        /api/v1/operations
GET        /api/v1/operations/{operation_id}
```

Tenant routes:

```text
GET        /api/v1/tenants/{tenant_id}/runner-nodes
GET/POST   /api/v1/tenants/{tenant_id}/endpoints
GET/PATCH  /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}
DELETE     /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}
POST       /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/rebuild
GET        /api/v1/tenants/{tenant_id}/operations
GET        /api/v1/tenants/{tenant_id}/operations/{operation_id}
```

Authorization:

- `platform_admin`: enrollment, full node lifecycle, platform operations, and emergency force-finalization. It still cannot retrieve tenant credentials.
- `tenant_admin`: create/update/delete/rebuild fixed endpoints and read tenant operations/eligible nodes.
- `operator`: stop/start/rebuild endpoints and read status/operations, but cannot change account or node binding.
- `auditor`: safe read-only endpoint, node projection, and operation metadata.
- `consumer`: denied management access.
- Agent session: only gRPC methods for its bound node.

All writes require idempotency keys. Endpoint PATCH/DELETE requires the expected resource version. Long-running writes return `202 Accepted` with the operation resource. Safe reads return desired and observed state separately.

## 11. Audit and observability

Audit and Outbox events include:

- Enrollment created, consumed, revoked, and rejected.
- Node registered, session rotated/revoked, maintenance changed, stale/offline, and drain requested/completed.
- Endpoint created, desired state changed, rebuild requested, deletion requested, and finalized.
- Runner command queued, delivered, acknowledged, succeeded, failed, garbage-collected, and force-finalized.
- Capacity reserved, renewed, released, and retained because node state is uncertain.

Audit details contain identifiers, generations, state transitions, attempt counts, protocol revision, and stable reason categories only. Agent messages, Docker errors, filesystem paths, runtime IDs, host addresses, credential fields, ciphertext, and configuration bytes are forbidden.

Metrics cover node connectivity, heartbeat age, active Runner count, work queue age, reconciliation latency, operation results, command retries, and finalizer age. Tenant IDs, endpoint IDs, account IDs, node names, and operation IDs are not metric labels.

## 12. Exit criteria

Phase 4 is complete when:

1. Current and previous protocol revision fixtures connect successfully; unsupported revisions fail safely.
2. Enrollment is one-time and expiring; Agent sessions rotate and are revoked with node disablement.
3. Repeated create/rebuild/stop commands converge to one Runner and one final observed generation.
4. Agent restart reconstructs Runner inventory without creating duplicates.
5. Control-plane restart resumes durable work and operations without losing finalizers.
6. Endpoint deletion drains and removes the Runner before finalization and releases capacity exactly once.
7. A stale/offline node cannot cause account-capacity oversell or automatic duplicate Runner creation.
8. Two tenants cannot inspect or modify each other's endpoint/operation rows, Runner files, cache directories, containers, or network namespaces.
9. Credential canaries are absent from PostgreSQL work tables, HTTP/gRPC errors, logs, audit, Outbox, traces, metrics, runtime metadata, and persistent disk.
10. Migration `8 -> 9 -> 8 -> 9`, PostgreSQL restart, Agent restart, duplicate delivery, Docker restart, race, clippy, Staticcheck, protobuf compatibility, and multi-architecture image gates pass.
11. No Gateway listener, proxy credential, traffic policy, Redis lease, pool scheduler, or automatic failover behavior is introduced.

