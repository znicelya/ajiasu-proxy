# Gateway Phase 5 operations

Phase 5 runs one active Gateway when exact aggregate connection and traffic
limits are required. Multiple Gateway identities are supported by the control
protocol, but counters are per instance until Phase 6 adds fencing and global
allocation.

## Enrollment and rotation

Create a one-time enrollment token with the control plane, bind it to the
Gateway certificate fingerprint, and consume it once. The Gateway stores only
the session verifier and the latest safe route snapshot. Rotate the Gateway
certificate by enrolling a replacement instance, waiting for a fresh snapshot,
then revoking the old session.

## Recovery

On control-stream loss, stop accepting routes whose grant or credential has
expired. Reconnect and require a complete snapshot before applying deltas.
Out-of-order snapshots/deltas are rejected. A Runner generation change also
invalidates every previous grant for that endpoint.

## Limits and observability

Connection, rate, idle, per-connection byte, and traffic-window limits are
enforced in the Gateway. Usage windows are batched to PostgreSQL with additive
upserts. Metrics contain bounded reason counters only; do not add domains, IPs,
usernames, credential IDs, route tickets, request bodies, or raw upstream
errors to logs, traces, or metric labels.

## Security boundaries

The Gateway has no Docker socket, PostgreSQL credentials, or AJiaSu credential
provider access. Agent relay authorization checks the signed audience,
endpoint, Runner generation, protocol, policy hash, and expiry before opening a
per-Runner socket. Credential material remains in the separate 0400 runtime
file; relay sockets live under `/run/ajiasu-relay`.

## Operational checks

Run `scripts/gateway-ci.ps1` before deployment. Confirm the route-cache age,
active connection count, auth failure bucket count, relay-unavailable count,
stale-grant count, and usage flush lag. A stale or revoked session is a safe
fail-closed condition: drain listeners and re-enroll rather than bypassing the
control plane.
