# Phase 5 Rust Proxy Gateway and Access Policy Design

## 1. Goal

Phase 5 adds the first data-plane surface to the platform: a Rust Gateway that
accepts HTTP forward-proxy, HTTPS CONNECT, and SOCKS5 TCP CONNECT traffic,
authenticates endpoint-specific proxy credentials, evaluates tenant policy, and
relays approved streams through the selected Phase 4 Runner.

Phase 5 keeps endpoint identity and Runner lifecycle authoritative in the Phase
4 control plane. It adds access configuration, proxy credentials, a Gateway
control protocol, a signed Runner route grant, and a private Agent-to-Runner
relay. The Gateway never receives the Docker socket, AJiaSu credentials, or a
tenant management session.

## 2. Scope and exclusions

Included:

- Rust Gateway process with bounded HTTP/1.1 proxy, HTTPS CONNECT, and SOCKS5
  TCP CONNECT listeners.
- One-time endpoint proxy credential issuance, Argon2id verifier storage,
  rotation, expiration, and revocation.
- Fixed endpoint access profiles: supported protocols, source CIDRs, target
  CIDRs/domains, ports, DNS mode, connection/rate/idle/byte limits.
- Non-overridable platform safety denies for loopback, link-local, metadata,
  control-plane, database, and configured management networks.
- Gateway enrollment and a mutually authenticated Gateway control stream for
  route snapshots and policy updates.
- Short-lived signed route grants bound to Gateway, tenant, endpoint, Runner,
  generation, and protocol.
- Agent relay transport and a Runner-local relay socket. The relay carries raw
  approved TCP bytes and never exposes a Runner address to tenants.
- Per-Gateway connection/rate/byte accounting, bounded auth work, metrics,
  safe audit events, restart-safe route snapshots, and failure tests.

Explicitly excluded:

- Pool scheduling, Redis leases, fencing tokens, account failover, node
  migration, quarantine automation, or global multi-Gateway allocation. Those
  are Phase 6 responsibilities.
- SOCKS5 UDP ASSOCIATE, SOCKS5 BIND, transparent proxying, TUN/WireGuard,
  HTTP/2 proxying, WebSocket proxying, TLS interception, certificate minting,
  content inspection, caching, or request-body logging.
- Direct Gateway access to Docker, PostgreSQL, encrypted AJiaSu credentials,
  Runner container IDs, host paths, or Runner network namespace handles.
- Arbitrary tenant policy languages, regular expressions, JavaScript, CEL, or
  user-provided resolver code.
- Seamless migration of established TCP connections during Runner or Gateway
  failure.

Phase 5 accepts only `binding_mode=fixed` endpoints created by Phase 4. A pool
binding is rejected with a stable `pool_binding_not_supported` error until
Phase 6.

## 3. Architecture and trust boundaries

The control plane owns endpoint access configuration, credentials, policy
versions, Gateway enrollment, Runner route grants, and audit records. The
Gateway owns listener sockets, proxy authentication, policy evaluation, and
connection accounting. The Agent owns Docker and the Runner-local relay. The
Runner owns the AJiaSu process and its network namespace.

```text
Client
  │ HTTP forward / HTTPS CONNECT / SOCKS5 CONNECT
  ▼
Rust Gateway
  │ credential verifier + compiled policy + route snapshot
  │ mTLS + signed route grant
  ▼
Node Agent relay
  │ private per-Runner Unix socket
  ▼
Runner-local relay ── AJiaSu process ── destination
```

The Gateway-to-Agent channel is a dedicated mTLS relay service. It is not the
Phase 4 command stream and does not carry AJiaSu credential configuration. A
route grant is signed by a control-plane Ed25519 key, expires quickly, and is
bound to the exact Runner generation. The Agent verifies the grant before
opening the private Runner socket. The Gateway cannot select an arbitrary
Runner by changing a route snapshot field.

The Runner relay runs inside the Runner container and listens on a per-Runner
Unix socket mounted only into that container and the local Agent boundary. The
socket directory contains no credential material. The Agent is the only
component allowed to open the host-side socket. The Gateway never receives the
socket path.

Phase 5 uses one active Gateway instance for exact aggregate connection and
traffic limits. Multiple Gateway identities and route versions are supported by
the protocol, but cross-instance global limits and placement are deferred to
Phase 6. A deployment that runs multiple Gateways must treat Phase 5 counters
as per-instance limits and document that mode explicitly.

## 4. Gateway control and relay protocols

### 4.1 Gateway control protocol

`api/proto/gateway/v1/gateway.proto` defines a versioned bidirectional
`GatewayControl.ControlStream`. A Gateway enrollment is one-time and produces a
short-lived service session, matching the Phase 4 Agent enrollment model.

Gateway-to-control-plane messages:

- `hello`: Gateway ID, instance ID, protocol revision, build version, and
  supported listener protocols.
- `snapshot_ack`: snapshot version, applied policy count, and safe failure code.
- `route_health`: bounded counts for route unavailable, relay unavailable, and
  stale grant failures.
- `heartbeat`: instance liveness and bounded active-connection counters.

Control-plane-to-Gateway messages:

- `route_snapshot`: complete replacement snapshot with a monotonic version.
- `route_delta`: add/update/revoke a single endpoint route.
- `route_grant_refresh`: a new signed grant for an unchanged Runner generation.
- `shutdown`: stop accepting new connections after a deadline.

Snapshots contain only data needed by the data plane:

- tenant and endpoint opaque IDs;
- enabled protocols and policy version/hash;
- proxy credential public IDs, Argon2id verifiers, expiry, and revocation state;
- selected Runner ID/generation and Agent relay identity/address;
- signed route grants and their expiry.

Snapshots never contain AJiaSu usernames/passwords, encrypted account fields,
raw request bodies, container IDs, Docker paths, or arbitrary Agent diagnostics.
The Gateway keeps the latest valid snapshot in memory and may persist an
encrypted restart cache containing only the same safe route data. A stale cache
cannot be used after its snapshot or grant expiry.

### 4.2 Agent relay protocol

`api/proto/relay/v1/relay.proto` defines `RunnerRelay.Open`, a bidirectional
stream with a metadata frame followed by bounded data frames and half-close
markers. The metadata frame contains:

- signed route grant;
- protocol (`http`, `connect`, or `socks5`);
- target host and port after Gateway policy evaluation;
- DNS mode and a request nonce.

The relay rejects malformed frames, oversized metadata, expired grants,
generation mismatches, unsupported protocols, disallowed target ports, and
more than one metadata frame. It applies the platform safety deny set again
after any Runner-side DNS resolution. Data frames have a fixed maximum size and
the stream has an idle deadline. Relay errors are stable categories only.

The Agent relay listener authenticates Gateway certificates against the
platform Gateway CA and checks the Gateway ID in the grant audience. A Gateway
session cannot call Agent management methods or Docker APIs.

## 5. Proxy credentials

Each endpoint may have zero or more active proxy credentials. A credential has:

- UUIDv7 ID and a non-secret public identifier used as the HTTP/SOCKS username;
- Argon2id verifier for the generated password;
- creation, expiry, revocation, and last-used timestamps;
- optional source-CIDR restriction that is intersected with the endpoint policy.

Create and rotate return the username and password exactly once. Later GET/list
responses return only the credential ID, public identifier, state, expiry, and
timestamps. The plaintext password is never placed in an operation, audit
detail, Outbox payload, route snapshot log, metric label, or database column.

The Gateway indexes credentials by public identifier before performing Argon2id
verification. Unknown identifiers take the same bounded failure path as a
wrong password. A semaphore caps concurrent Argon2id work; per-source and
per-credential failure buckets prevent a client from exhausting Gateway CPU.
Successful authentication produces an in-memory access context containing only
tenant ID, endpoint ID, credential ID, source address, and policy version.

Credential revocation reaches Gateways through a route delta. A Gateway must
reject a revoked credential after the delta is applied and must reject it after
the credential expiry even if the control stream is unavailable.

## 6. Endpoint access profile

Migration 10 adds an access profile without changing the Phase 4 endpoint ID:

- `protocols`: non-empty subset of `http`, `connect`, `socks5`;
- `dns_mode`: `gateway` or `runner`;
- `source_cidrs`: optional allowlist evaluated against the real peer address;
- `target_allow_cidrs` and `target_deny_cidrs`;
- `target_allow_domains` and `target_deny_domains`, canonicalized to lowercase
  IDNA names with an explicit exact/suffix match rule;
- `allowed_ports`: normalized inclusive ranges;
- `max_connections`, `max_connection_rate`, `idle_timeout`;
- `max_bytes_per_connection` and an optional traffic-quota window;
- `policy_version`, canonical document hash, created/updated timestamps.

The API accepts typed JSON and compiles it to canonical JSON before storage.
Unknown fields, overlapping contradictory ranges, invalid CIDRs, wildcard
domains, unsafe DNS modes, and limits outside platform bounds are rejected.
Tenant allow rules can narrow platform behavior but cannot remove a platform
safety deny.

Policy evaluation order is fixed:

1. listener/protocol and credential state;
2. source-CIDR intersection;
3. canonical target parsing and port range;
4. non-overridable platform safety denies;
5. endpoint deny rules;
6. endpoint allow rules, if an allowlist is present;
7. connection, rate, idle, and byte budgets.

An empty target allowlist means public destinations are allowed after safety
denies. A non-empty allowlist becomes an allowlist. An explicit deny always
wins over a tenant allow. DNS names are normalized before matching, and every
resolved A/AAAA result is checked; a single unsafe result fails the request.

## 7. Protocol behavior

### 7.1 HTTP forward proxy

- Accept HTTP/1.1 absolute-form requests only.
- Require `Proxy-Authorization: Basic ...`; reject origin `Authorization` from
  being used as proxy authentication.
- Enforce request-line, header-count, header-size, and body streaming limits.
- Remove proxy hop-by-hop headers before forwarding and preserve only an
  approved end-to-end header set.
- Resolve and policy-check the absolute target before opening the relay.
- Do not buffer or log request/response bodies.

### 7.2 HTTPS CONNECT

- Parse authority-form `host:port`, including bracketed IPv6.
- Apply policy before the 200 response. A failed relay never receives a false
  success response.
- Tunnel bytes without TLS termination or certificate inspection.
- Support half-close and bounded bidirectional copy with idle and total-byte
  deadlines.

### 7.3 SOCKS5 TCP CONNECT

- Implement RFC 1928 greeting and RFC 1929 username/password authentication.
- Support IPv4, IPv6, and domain-name targets.
- Support `CONNECT` only; reject `NO AUTH`, `GSSAPI`, `UDP ASSOCIATE`, and
  `BIND` with stable protocol replies.
- Enforce handshake and address-length bounds before allocation.
- Return success only after the relay target is connected.

Malformed or slow clients are closed with bounded work. Gateway tasks do not
spawn unbounded per-byte or per-header allocations.

## 8. DNS and SSRF safety

`gateway` DNS mode resolves through a controlled Gateway resolver. It checks
all answers and passes a selected safe IP to the Runner relay. `runner` DNS mode
passes the canonical hostname to the Runner relay; the relay resolves inside
the AJiaSu network path and applies the platform safety CIDR set to every
answer before connecting.

The immutable safety deny set includes loopback, unspecified, link-local,
multicast, RFC1918/RFC4193 private ranges, cloud metadata ranges and host
management ranges configured by the platform. The platform may add denies but
cannot remove these defaults through a tenant API. Numeric IPs and DNS names
are evaluated through the same canonical path, preventing a hostname rule from
being bypassed with an equivalent IP or an unsafe DNS rebinding result.

## 9. Connection accounting and failure behavior

The Gateway uses bounded atomic counters and token buckets keyed by endpoint and
credential public ID. A connection consumes a permit before relay creation and
returns it on every close path. Rate and byte counters are updated on both
directions, including half-close and error paths. A traffic window is flushed
as an aggregate to the control plane; the Gateway stops new connections when a
durable quota budget is exhausted and never logs destination labels.

When a route is missing, stale, revoked, or the Runner is not observed running,
new clients receive stable `proxy_endpoint_unavailable` or
`proxy_endpoint_not_ready` responses. Existing streams are not silently moved
to another Runner. Agent or control-plane disconnects stop new connections
after the last valid grant expires while existing streams continue until their
idle/deadline budget.

Gateway restart starts with no usable route cache unless a persisted snapshot is
still within its signature and expiry window. Control-plane restart rebuilds
snapshots from PostgreSQL and replays them after Gateway registration.

## 10. Control-plane API and schema

Platform routes:

```text
POST/DELETE /api/v1/gateway-enrollments[/{enrollment_id}]
GET         /api/v1/gateways
GET/PATCH   /api/v1/gateways/{gateway_id}
```

Tenant routes:

```text
GET/PATCH   /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/access
GET/POST    /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials
POST        /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials/{credential_id}/rotate
DELETE      /api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials/{credential_id}
```

Access changes and credential changes use Phase 2 idempotency, CSRF, optimistic
version, stable errors, and audit conventions. `tenant_admin` manages access
profiles and credentials; `operator` may read safe status and revoke/rotate
only when explicitly granted the operation permission; `auditor` may read safe
metadata; `consumer` receives no management access. Gateway service identity
can only call its control and relay protocols.

Schema 10 adds forced-RLS tenant tables for access profiles and proxy
credentials, platform tables for Gateway enrollment/session/health, signed
route-grant metadata, and bounded traffic-usage windows. Tenant foreign keys
include tenant identity. No table stores plaintext proxy or AJiaSu passwords.

## 11. Observability and audit

Audit records cover access profile create/update, proxy credential issue/rotate/
revoke/expire, Gateway enrollment/session changes, route snapshot apply, and
quota exhaustion. Proxy connection attempts are represented by counters and
sampled stable reason categories, not one audit row per request.

Metrics include active connections, handshake latency, auth failures, policy
denials by stable category, relay failures, bytes, idle timeouts, route age,
snapshot version, and quota budget. They do not include tenant IDs, credential
IDs, usernames, destination domains, IPs, URLs, or request IDs as labels.

Logs and traces contain listener, protocol, instance, endpoint opaque ID only
where operationally necessary, and stable error codes. They never contain
Proxy-Authorization values, SOCKS passwords, target URLs, headers, bodies,
DNS answers, route tickets, socket paths, or relay payloads.

## 12. Compatibility and supply chain

- Gateway control protocol supports current and immediately previous revision.
- Phase 4 Agent control revision is extended additively for route/relay
  capabilities; revision 2 Agents remain valid for control-only operation and
  cannot receive Phase 5 relay commands.
- Gateway and relay protobuf descriptors are generated from checked-in sources;
  Buf lint/breaking checks protect the compatibility window.
- Rust Gateway, proxy-protocol, policy, and relay crates use locked
  dependencies, clippy warnings denied, property/fuzz tests for parsers, and
  reproducible multi-architecture images.
- Gateway images are non-root, read-only-root where feasible, and have no
  Docker socket. Runner image changes are separately digest locked.

## 13. Phase 5 exit criteria

1. HTTP forward, HTTPS CONNECT, and SOCKS5 TCP CONNECT compatibility suites
   pass, including malformed, slow, half-close, and oversized clients.
2. Proxy credential plaintext is returned once, verifiers are tenant-scoped,
   revoked/expired credentials stop working after route delta application, and
   canaries are absent from logs, traces, audit, snapshots, and disk.
3. Platform safety denies cannot be weakened by tenant policies; DNS rebinding,
   private ranges, metadata targets, and numeric-IP bypasses are blocked.
4. A fixed endpoint routes only to the matching Runner ID and generation through
   the Agent relay; no Docker socket or Runner address reaches the Gateway API.
5. Connection, rate, idle, and traffic limits remain bounded under concurrent
   clients and every close/error path releases permits.
6. Gateway, control plane, Agent, and Runner relay restart without accepting an
   expired or stale route grant and without exposing a stale Runner.
7. Cross-tenant credential, policy, route snapshot, cache, metric, log, and
   error isolation tests pass.
8. Migration `9 -> 10 -> 9 -> 10`, current/previous protocol compatibility,
   race, static, parser property/fuzz, SBOM, vulnerability, and multi-arch
   image gates pass.
9. No pool scheduling, Redis fencing, automatic failover, TLS interception,
   SOCKS5 UDP, or management credential reuse is introduced.
