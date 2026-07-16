# Phase 7 Docker Compose Production Package Design

## 1. Goal

Phase 7 turns the Phase 6 control plane, scheduler, Gateway, Agent, and Runner
components into a reproducible Docker Compose package that can be installed on
a clean Linux host, operated without repository secrets, exercised with fake
AJiaSu boundaries, upgraded, backed up, restored, rolled back, and shut down
without weakening the security boundaries established in Phases 1-6.

Compose is a packaging and operationalization layer. PostgreSQL remains the
authoritative business-state store, Redis remains reconstructable
coordination, the Agent remains the only component with Docker Engine access,
and Runners remain isolated one-connection workloads created dynamically by
the Agent rather than permanent Compose services.

The same image names, configuration names, secret-file conventions, health
semantics, ports, and resource meanings are intended to be reused by the Phase
8 Helm chart.

## 2. Current readiness findings

The repository has secure component foundations but is not yet deployable as
an end-to-end Compose product:

- there is no canonical Compose topology, installer, release manifest, or
  production configuration template;
- the Gateway binary currently validates configuration but does not yet run
  its control stream and proxy listeners;
- the control-plane runtime does not yet expose the Gateway control service;
- Agent-to-Runner relay wiring is not complete enough for a packaged proxy
  smoke test;
- database DSNs and first-enrollment tokens still have environment-variable
  paths that can expose secrets through container inspection;
- Gateway and some Agent build/runtime images are not uniformly locked by
  digest or verified for both supported architectures;
- the control-plane binary has no non-interactive migration/status command for
  a one-shot Compose job;
- backup, restore, upgrade, rollback, graceful-drain, and Compose security
  verification scripts do not exist.

Phase 7 may close these packaging blockers. It must not redesign scheduling,
tenant policy, proxy semantics, or health state machines already owned by
Phases 2-6.

## 3. Scope and exclusions

Included:

- canonical development, single-host, and external-dependency Compose modes;
- immutable multi-architecture images for Control Plane, Gateway, Agent, and
  Runner;
- optional pinned PostgreSQL, Redis, and development identity-provider
  services;
- production secret generation and file-backed sensitive configuration;
- one-shot migration, bootstrap, enrollment, backup, restore, upgrade,
  rollback, status, and shutdown workflows;
- fixed and pool endpoint smoke tests for HTTP forwarding, HTTPS CONNECT, and
  SOCKS5 CONNECT using fake AJiaSu credentials and a controlled fake Runner;
- Docker security, restart, dependency-loss, backup/restore, and upgrade
  rehearsals in CI;
- documentation and a compatibility manifest suitable for reuse by Phase 8.

Explicitly excluded:

- Kubernetes resources, Helm templates, CRDs, workload identity, Pod security
  policy, cluster autoscaling, and multi-node Kubernetes testing;
- the complete Web Console, dashboards, alert rules, SIEM export, and release
  portal, which remain Phase 9;
- global multi-Gateway traffic counters. Exact aggregate limits still require
  one active Gateway;
- high-availability PostgreSQL or Redis orchestration inside Compose;
- transparent TLS interception, SOCKS5 UDP/BIND, multi-region placement, and
  seamless migration of established TCP streams;
- storing real AJiaSu credentials in CI or generated example files.

The roadmap mentions a Console in the Compose inventory, but the application
Console is delivered in Phase 9. Phase 7 reserves an optional `console`
profile and image/configuration contract without shipping a placeholder that
could be mistaken for the production Console.

## 4. Deployment invariants

1. No repository file, image layer, Compose model, container environment,
   command line, log, or CI artifact contains a production credential,
   encryption key, enrollment token, route signature, or database password.
2. Production services consume sensitive values from mounted files. A `_FILE`
   form is preferred wherever a value is secret-bearing.
3. Every release image and dependency image is selected by immutable digest.
   Mutable tags are allowed only as reviewed inputs to the lock-update tool.
4. Only the Agent receives Docker Engine access. Control Plane, Gateway,
   dependency services, migration jobs, backup jobs, and future Console
   services never receive the socket.
5. No service uses `privileged`, host PID, host IPC, or broad capabilities.
   Runners use `network_mode=none`, read-only root filesystems, dropped
   capabilities, non-root UID 65532, bounded resources, and private tmpfs.
6. PostgreSQL committed state and the matching encryption key are the critical
   backup set. Redis data is never restored as authoritative state.
7. Migrations complete successfully before a new Control Plane becomes ready.
   Schema 11 and schema-10 binaries are not mixed.
8. Development shortcuts are isolated by profile and cannot be enabled by a
   production environment file accidentally.
9. Compose and later Helm packaging use the same configuration names, ports,
   secret mount paths, health outcomes, and image entrypoints.
10. A Compose success claim requires an actual proxy request through Gateway,
    Agent relay, and a fake Runner; container health alone is insufficient.

## 5. Supported deployment modes

### 5.1 Development mode

Development mode runs pinned PostgreSQL, Redis, a development identity
provider, Control Plane, one Gateway, one Agent, and fake test boundaries. It
may use loopback-only insecure Agent transport where existing configuration
explicitly permits it. Generated development secrets are local, ignored by
Git, and never reused for production.

### 5.2 Single-host production mode

Single-host mode runs pinned PostgreSQL and Redis containers with named
volumes. It is a supported small-deployment topology, not an HA database
offering. The operator must accept host-level failure scope and configure
encrypted off-host backups. Only one active Gateway is started for exact
aggregate limits.

### 5.3 External-dependency production mode

External mode runs application components while PostgreSQL, Redis, OIDC, TLS,
and backup retention are provided by managed or separately operated services.
It is the recommended production mode and the closest semantic match for the
Phase 8 Helm chart.

The canonical file layout is:

```text
deploy/compose/
  compose.yaml
  compose.dependencies.yaml
  compose.development.yaml
  compose.production.yaml
  env/compose.env.example
  config/
  generated/                 # ignored; created by init
  release-manifest.example.json
```

`compose.yaml` owns stable service names, networks, volumes, health checks, and
secret mount paths. Overlays may add dependencies, development-only ports, or
production constraints but cannot redefine security-critical ownership.

## 6. Service topology

```text
                          public clients
                                |
                      published Gateway ports
                                |
                         +-------------+
                         |   Gateway   |
                         +------+------+ 
                                | control/relay network
                 +--------------+---------------+
                 |                              |
          +------+-------+               +------+------+
          | Control Plane|               |    Agent    |
          +------+-------+               +------+------+
                 | dependencies                 | Docker API
          +------+------+                        |
          |             |                 dynamic Runners
     PostgreSQL       Redis               network=none
```

Networks are separated by purpose:

- `edge`: published Gateway listeners only;
- `control`: authenticated Control Plane, Gateway, and Agent control traffic;
- `dependencies`: Control Plane to PostgreSQL/Redis and development OIDC;
- no general network is attached to dynamically created Runners.

Persistent storage is also ownership-scoped:

- PostgreSQL owns authoritative state, including the current audit/outbox
  tables; Phase 9 may add separate audit export/archive storage;
- Agent state retains only node/session/inventory identity needed for restart;
- generated keyring, certificates, and secret files remain separate from the
  PostgreSQL volume and are backed up under stricter access control;
- Redis, Gateway route cache, and per-Runner runtime files are reconstructable;
- AJiaSu cache is not shared across tenants or Runners. If a per-Runner cache
  is required, it has the same owner/lifetime as that Runner and is deleted by
  owned-resource cleanup.

The Control Plane management HTTP port is loopback-bound by default in the
single-host package and is expected to sit behind an operator-managed HTTPS
reverse proxy in production. Phase 7 does not introduce a mutable, unpinned
ingress image as an implicit trust boundary.

## 7. Image and release contract

A release manifest maps each service to an immutable image reference, target
architectures, source revision, protocol revision, schema version, and
configuration revision. The Compose package refuses missing digests,
unsupported architectures, a mutable Runner image, and manifest/schema
mismatches.

All Dockerfiles must:

- use digest-locked build and runtime images;
- build `linux/amd64` and `linux/arm64`;
- run non-root where the service does not require host Docker access;
- include OCI source/revision/version labels;
- expose a bounded `healthcheck` or `version` command without printing secrets;
- use read-only roots and writable tmpfs/volumes only where documented;
- contain no default credentials or development enrollment tokens.

The Agent may need the host Docker socket and the socket's numeric group ID.
Compose supplies the group explicitly; it does not run the Agent privileged or
as host root. Rootless Docker is supported only when the configured socket is
reachable and the runtime contract tests pass.

## 8. Configuration and secret lifecycle

`compose-init` creates a private generated directory with restrictive modes and
atomic writes. At minimum it creates:

- the Control Plane keyring;
- PostgreSQL application-role passwords or external DSN files;
- Redis ACL password file;
- OIDC client secret reference;
- internal Agent-control CA/certificate/key material for production mode;
- one-time Agent and Gateway enrollment inputs;
- a release/environment identifier used in lease namespaces and labels.

Secret files are never interpolated into `docker compose config` output.
Database DSNs, enrollment tokens, session tokens, and any future Gateway
credentials gain file-backed configuration. The bootstrap flow removes or
revokes one-time enrollment material after durable session state is stored.

Non-secret settings live in a reviewable environment file. The configuration
matrix records the exact Compose variable, container environment name, secret
mount path, default policy, and future Helm value mapping.

## 9. Lifecycle and migration model

Startup order is explicit:

1. validate Docker/Compose versions, host architecture, release manifest,
   generated file permissions, port availability, and Docker socket scope;
2. start optional dependencies and wait for bounded health checks;
3. run a one-shot, advisory-locked migration job and verify schema 11;
4. start Control Plane and wait for `/readyz`;
5. run interactive break-glass bootstrap only when explicitly requested;
6. create/consume one-time Agent and Gateway enrollments;
7. start Agent and Gateway, require current sessions and route snapshot;
8. run a protected smoke request before declaring the stack usable.

Migration commands are non-interactive, bounded by timeout, and serialize on a
PostgreSQL advisory lock. Application services never race to apply migrations.

Shutdown order stops new Gateway connections first, drains within a configured
deadline, stops scheduler mutations, allows Agent observations/finalizers to
converge, then stops Control Plane and dependencies. Forced timeout is visible
as a failed operation, not reported as a clean shutdown.

## 10. Runtime security boundary

Compose security tests inspect the rendered model and running containers. They
must prove:

- Docker socket mounts appear only on the Agent;
- no service is privileged or uses host network/PID/IPC;
- capabilities are dropped and `no-new-privileges` is set;
- read-only roots and bounded tmpfs/volumes are present where required;
- secrets are file mounts and do not appear in `docker inspect`, logs, labels,
  health commands, or rendered configuration;
- dependency ports are not publicly published in production mode;
- generated state directories are excluded from Git and CI artifacts;
- Runner labels, limits, image digest, network isolation, and cleanup match the
  Agent ownership contract.

Mounting the Docker socket is a high-authority operation even when limited to
the Agent. The runbook must state that host compromise of the Agent implies
Docker-host compromise and must not describe the socket as read-only safety.

## 11. Backup and restore

The critical recovery set consists of:

- PostgreSQL data including the Goose version table, audit/outbox rows, and all
  tenant state;
- the exact Control Plane keyring required to decrypt account credentials;
- release manifest, non-secret configuration, CA material needed to validate
  service identity, and backup metadata.

Redis, active leases, session caches, ephemeral Runner files, and route caches
are reconstructed and are not restored from backup.

The backup command writes to an operator-selected destination with restrictive
permissions, records checksums and schema/release metadata, and never silently
packages the database dump and keyring into the same unprotected file. Off-host
encryption and retention are mandatory for production.

Restore runs only into an empty or explicitly disposable environment. It
verifies checksums, schema compatibility, keyring identity, ownership, and
secret modes before starting application services. The exercise must prove a
known encrypted test credential can be decrypted with the restored key and
cannot be decrypted with a different key.

Phase 7 targets the program RPO of 15 minutes and RTO of 60 minutes in a
documented single-host rehearsal. Managed external services may use provider-
specific PITR, but the same validation and keyring requirements apply.

## 12. Upgrade and rollback

Upgrade is manifest-driven:

1. validate target images and compatibility metadata;
2. take and verify a pre-upgrade backup;
3. stop unsafe writes and drain Gateway traffic;
4. apply migrations through the one-shot job;
5. recreate services by dependency order;
6. wait for readiness, current Agent/Gateway sessions, assignment convergence,
   and a fixed/pool smoke test;
7. record the accepted manifest.

Rollback never means only changing image tags. It verifies the previous
manifest, schema compatibility, database backup, keyring, and Runner image.
Because the current Control Plane requires an exact schema, rolling a schema-11
deployment to a schema-10 binary requires the documented database rollback or
restore procedure and cannot be mixed in place.

## 13. Health and observability

Container health commands expose only bounded local status:

- Control Plane liveness/readiness and schema/dependency state;
- Gateway process, control-stream snapshot freshness, and listener state;
- Agent process, session freshness, Docker runtime reachability, and inventory
  convergence;
- PostgreSQL and Redis dependency health in bundled profiles.

Health output and Compose logs contain stable categories only. They never emit
DSNs, passwords, enrollment/session tokens, Redis values, proxy targets, DNS
answers, or arbitrary upstream errors. Log rotation is configured for bundled
services. Full dashboards, alert routing, and trace backends remain Phase 9.

## 14. End-to-end and failure testing

Normal CI uses a fake AJiaSu image and generated test credentials. It covers:

- clean-host init, pull/build, migration, bootstrap, start, readiness, and stop;
- fixed and pool endpoints through HTTP forwarding, HTTPS CONNECT, and SOCKS5
  CONNECT;
- duplicate/reordered control events and Gateway snapshot recovery;
- Runner restart, Agent restart, Control Plane restart, Gateway restart, and
  Docker daemon reconnect;
- Redis loss/recovery without unsafe pool allocation;
- PostgreSQL interruption without corrupting committed state;
- backup, destructive teardown, restore, key verification, and smoke replay;
- upgrade from the previous manifest and rollback rehearsal;
- cross-tenant request, cache, volume, log, and error isolation;
- rendered and runtime Docker security inspection;
- bounded shutdown with no orphaned platform-owned Runner containers.

Real AJiaSu account tests remain protected by the usage gate and are not a
pull-request requirement.

## 15. Exit criteria

Phase 7 is complete when:

1. A clean supported Linux host can initialize and start the development and
   single-host profiles from documented commands.
2. External PostgreSQL/Redis production configuration uses the same component
   images and secret-file conventions.
3. Fixed and pool proxy smoke tests pass for HTTP, CONNECT, and SOCKS5 through
   the actual packaged component path.
4. No secret is present in the repository, image history, Compose rendering,
   container environment, inspection output, or logs.
5. Only the Agent has Docker Engine access; all other runtime security
   assertions pass.
6. Redis loss blocks unsafe allocation while existing safe traffic follows the
   Phase 6 degraded-mode contract.
7. Backup/restore and upgrade/rollback rehearsals complete with verified data
   and keyring compatibility.
8. Graceful shutdown leaves no orphaned owned Runner and no falsely successful
   incomplete operation.
9. Multi-architecture image, SBOM, vulnerability, Compose config, E2E, race,
   clippy, Staticcheck, and secret-scanning gates pass.
10. Phase 8 can consume the image/configuration/health contract without
    changing application semantics, and no Phase 9 Console implementation is
    introduced.
