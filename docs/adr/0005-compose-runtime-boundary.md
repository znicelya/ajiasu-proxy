# ADR 0005: Docker Compose runtime and release boundary

## Status

Accepted for Phase 7.

## Context

The Phase 6 application components have independent runtime contracts, but a
release still needs one stable deployment boundary. Docker Compose must package
the Control Plane, Gateway, Agent, optional dependencies, and dynamically
created Runners without weakening the security and authority decisions from
Phases 1-6. The same names and ownership rules must also remain reusable by the
future Helm package.

## Decision

- The standing application service names are `migration`, `control-plane`,
  `gateway`, and `agent`. `postgres`, `redis`, and `identity-provider` are
  optional dependency-profile services. Runners remain Agent-owned dynamic
  containers; there is no standing `runner` Compose service.
- The `console` profile is reserved with no image or enabled service until
  Phase 9. A package that assigns an image to this profile is not a Phase 7
  release.
- `edge`, `control`, and `dependencies` are the stable network names. Gateway
  alone joins `edge`; Agent and Gateway reach the Control Plane on `control`;
  only dependency clients and dependency services join `dependencies`.
- The stable named volumes are `postgres-data`, `agent-state`, and
  `gateway-state`. Redis, Gateway route snapshots, leases, and Runner runtime
  files are reconstructable and are not authoritative backup inputs.
- Published ports are owned as follows: Gateway HTTP `8080/tcp`, Gateway
  SOCKS5 `1080/tcp`, and the loopback-only Control Plane management endpoint
  `127.0.0.1:8081/tcp`. Internal control ports are Control Plane Agent gRPC
  `9090/tcp` and Gateway gRPC `9091/tcp`; dependency ports are never published
  by a production profile.
- Production secret files use fixed mounts below `/run/secrets/ajiasu/`:
  `database-normal-dsn`, `database-platform-dsn`, `database-migration-dsn`, `redis-password`,
  `oidc-client-secret`, `control-plane-keyring`, `agent-enrollment-token`, and
  `gateway-enrollment-token`. Secret values and secret-bearing DSNs are not
  Compose variables, command arguments, image metadata, or committed defaults.
- Health outcomes are stable categories rather than free-form diagnostics:
  `live`, `ready`, `not_ready`, `degraded`, `draining`, and `stopped`.
  Container health is necessary but the package is not declared ready until a
  real proxy smoke request has crossed Gateway, Agent relay, and a Runner.
- Every application and dependency image is selected by repository plus
  `sha256` digest. The release manifest revision, source revision, supported
  platforms, schema version, protocol revisions, configuration revision,
  topology, and future Helm mappings are validated before startup.
- Only Agent may receive a configured Docker socket. No service may use
  privileged mode, host networking, host PID, host IPC, or an unbounded
  capability set.

The configuration matrix is the ownership registry for Compose variables,
container environment names, secret mounts, defaults, deployment modes, and
future Helm values. A configuration name without exactly one owner is invalid.

## Consequences

Compose overlays may add optional dependencies or tighten constraints, but
they cannot rename services, reassign ports, change secret mounts, or transfer
Docker authority. Phase 8 Helm values must map to the frozen configuration
names instead of inventing a second deployment API. Changes to the release
manifest or configuration matrix require a new supported revision and an
immutable compatibility fixture.
