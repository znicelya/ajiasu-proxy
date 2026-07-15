# ADR 0003: Gateway and Runner Relay Boundary

## Status

Accepted for Phase 5.

## Decision

The Rust Gateway is a trusted data-plane policy evaluator, but it never receives
the Docker socket, PostgreSQL credentials, encrypted AJiaSu credentials, or
Runner host paths. It receives only safe route snapshots and short-lived signed
grants over an mTLS Gateway control stream.

Approved traffic is relayed through the authenticated Agent relay. The Agent
verifies the Ed25519 grant and opens a private per-Runner Unix socket mounted
only into the Runner-local relay. The Gateway never learns the socket path or a
tenant-visible Runner address.

The Runner relay accepts one metadata frame followed by bounded TCP data and
half-close frames. It re-applies immutable platform destination denies after
Runner-side DNS resolution. It cannot access Docker or any other Runner socket.

## Consequences

- Gateway compromise does not grant Docker or AJiaSu credential access, but it
  remains a trusted policy enforcement boundary until its grants expire.
- Phase 5 supports one active Gateway for exact aggregate counters. Redis
  fencing and multi-Gateway global allocation are deferred to Phase 6.
- The relay protocol is separate from the Phase 4 Agent command stream so data
  bytes cannot be confused with lifecycle commands or persisted work items.
