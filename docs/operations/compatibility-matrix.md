# Compatibility Matrix

| Component | Current | Previous/rolling compatibility | Phase 7 package constraint |
| --- | --- | --- | --- |
| Control Plane schema | 11 | Schema 10 is rollback-only | Runtime readiness requires exactly schema 11; do not mix schema-10 and schema-11 control-plane replicas. |
| Scheduler protocol | revision 1 | No previous revision | Unknown revisions and stale generations are rejected. |
| Gateway control protocol | revision 1 | revision 1 | Assignment metadata is additive; Gateways require a snapshot after any delta gap. |
| Agent control protocol | revision 2 | revision 1 | Runner generation remains monotonic; relay authorization also requires current assignment generation and validity. |
| Relay protocol | revision 1 | revision 1 | Gateway and Agent reject unknown revisions, stale grants, and stale assignment or Runner generations. |
| OpenAPI | `/api/v1` | Additive changes only | Assignment get/reconcile routes are registered and checked against the OpenAPI document. |
| Redis | RESP2 with Lua scripting | Reconstruct from PostgreSQL after loss | Redis is coordination only; AUTH, SELECT, EVAL, GET, SET, DEL, INCR, and PEXPIRE are required. |
| PostgreSQL | 17 locked in CI | Migration rehearsal from schema 10 | Scheduler tables use forced RLS and PostgreSQL committed assignments are authoritative. |
| Gateway aggregate limits | one active Gateway for exact totals | Same as Phase 5 | Scheduler fencing does not yet provide global multi-Gateway traffic counters. |
| Compose release manifest | revision 1 | No previous revision | Unknown revisions, mutable or missing image digests, unsupported platforms, duplicate published ports, and topology drift are rejected before startup. |
| Compose configuration matrix | revision 1 | No previous revision | Every configuration has one component owner and a future Helm mapping; secret-bearing entries have file mounts and no committed value or default. |
| Container host OS | Linux kernel 5.15 or newer | Windows 11 and macOS development hosts may use a supported Docker Desktop Linux VM | Single-host and external-dependency production modes require a maintained 64-bit Linux host; native Windows containers are unsupported. |
| Host architecture | `linux/amd64`, `linux/arm64` | No 32-bit or non-Linux container targets | Every application image and locked dependency must contain the selected platform. |
| Docker Engine | 27.x or newer | Current and previous stable release channels | Linux containers, Compose v2, Buildx, health checks, read-only filesystems, tmpfs, and explicit socket group mapping are required. |
| Docker Compose | v2.33.1 or newer | Current stable v2 | Profiles, long-form `depends_on`, secrets, configs, health conditions, and `gw_priority` semantics used by the package must validate. |
| Docker Buildx | v0.19 or newer | Current stable | Multi-platform OCI output, provenance, and SBOM attestations are release gates. |
| Agent Docker authority | one Agent per Docker host | No read-only safety compatibility claim | Agent alone mounts the Docker socket; Agent compromise implies host compromise. |
| Compose lifecycle | init/start/status/drain/down revision 1 | Repeated start is idempotent | Lifecycle commands preserve diagnostics on timeout and remove Runners only after exact ownership validation. |
| Compose recovery | backup manifest revision 1 | Schema-11 database restore only | Database and matching keyring are both required; Redis/session/Runner caches are reconstructed. |

## Future Helm mapping

The Compose package intentionally adds no Kubernetes resources. A future Helm package maps Compose `services` to workloads/services, `networks` to NetworkPolicies, named `volumes` to PVCs, and secret/config file keys to Kubernetes Secrets/ConfigMaps. The current service names, network boundaries, volume ownership, health outcomes, and secret mount names are the compatibility contract for that future mapping.

Update this matrix whenever protocol or manifest revisions, schema readiness,
supported host tooling, Redis command requirements, or the exact-limit Gateway
constraint changes.
