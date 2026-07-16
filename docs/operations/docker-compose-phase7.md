# Phase 7 Docker Compose operations

This package runs Control Plane, one Agent, and one active Gateway with no standing Runner service. Runners are created on demand by Agent and are isolated per connection. Production hosts must be maintained 64-bit Linux systems with Docker Engine 27+, Compose v2.33.1+, Buildx v0.19+, sufficient disk for PostgreSQL/backups, and synchronized UTC time.

## Security boundary

Agent has read-write access to the Docker Engine socket. Compromise of Agent therefore implies compromise of the Docker host; a read-only socket mount would not make that authority safe. No other service receives the socket. Application containers run as `65532:65532`, drop all capabilities, use read-only roots, and enable `no-new-privileges`.

Exactly one active Gateway is supported when exact aggregate connection/byte limits are required. Multiple Gateways would enforce local limits independently and are not a supported scaling method in Phase 7. Scale accounts and Runner capacity through pools and Agent capacity instead.

Real AJiaSu accounts remain behind the usage gate. Compose E2E accepts only fake fixtures unless a separately protected real-account workflow is authorized.

## Modes and initialization

- `development`: bundled PostgreSQL/Redis and optional development identity provider.
- `single-host`: bundled PostgreSQL/Redis with production transport and cookie policy.
- `external`: production application topology with operator-managed PostgreSQL, Redis, and OIDC. Database DSNs require `sslmode=verify-full`; Redis TLS is mandatory.

Run `scripts/compose-init.ps1` once with immutable image digests, a unique lowercase environment ID, and the selected mode. External mode accepts only secret-file paths, never secret values. Generated state is private, checksummed, symlink-safe, Git-ignored, and idempotent; rerunning init never rotates keys.

Terminate TLS at a reviewed ingress in front of Gateway and the management endpoint. Internal Control Plane, Agent, Gateway, and relay links use the generated platform CA and mutual TLS. Do not publish PostgreSQL or Redis ports.

## Bootstrap, enrollment, and start

1. Start dependencies and migrations with `compose-up.ps1` or let that script perform the complete sequence.
2. Run `compose-admin-bootstrap.ps1` interactively for the one break-glass local administrator. TOTP and recovery codes are shown once.
3. Agent/Gateway enrollment is created automatically by `compose-up.ps1`, or explicitly with `compose-agent-enroll.ps1` and `compose-gateway-enroll.ps1`. Tokens are one-time and never printed by the scripts.
4. Supply a private smoke JSON file with `fixed` and `pool` proxy probes. Production startup is not accepted until both pass and Gateway has applied a current route snapshot.

`compose-up.ps1` validates Docker, immutable images, generated-state hashes, rendered Compose, dependency health, schema migration, Control Plane readiness, Agent/Gateway sessions, snapshot readiness, and smoke traffic. Failures preserve diagnostic containers and volumes.

## Status, degradation, drain, and shutdown

`compose-status.ps1` prints bounded component, session, and fixed/pool assignment categories without environment dumps or credentials. `ready` requires healthy Control Plane/Agent/Gateway, current sessions, and assigned fixed and pool routes.

Redis is coordination-only. During loss, new pool allocations stop; committed PostgreSQL assignments remain authoritative. After Redis returns, fencing tokens must advance and no capacity may be oversold. Never restore Redis data or manually seed lease keys.

Use the node API drain operation for planned capacity work. For stack shutdown, `compose-down.ps1` marks nodes/assignments draining, stops Gateway acceptance, waits for graceful service stops, validates the full Runner ownership-label set, removes only Runners belonging to the current node, and stops dependencies last. Timeout or ambiguous ownership exits nonzero and preserves diagnostics.

## Backup, restore, upgrade, and rollback

`compose-backup.ps1` writes a PostgreSQL custom dump, CA/configuration evidence, checksums, and a separately located keyring artifact. Redis, leases, sessions, Runner state, and route caches are excluded. Encrypt and retain database and keyring artifacts separately off host. Targets are RPO <= 15 minutes and rehearsed RTO <= 60 minutes.

`compose-restore.ps1` requires explicit `-Disposable`, verifies environment/schema/checksums/keyring, removes only the disposable PostgreSQL volume, restores, and leaves acceptance to the normal readiness and smoke gates. External databases use provider PITR with the same manifest/keyring verification.

`compose-upgrade.ps1` requires immutable target images and a verified pre-upgrade backup, then drains, migrates, restarts by dependency order, and accepts only after convergence and smoke. `compose-rollback.ps1` restores the pre-upgrade database and matching keyring before starting the previous manifest. Changing tags alone is never rollback.

## Troubleshooting and data removal

Use `docker compose ps`, bounded `compose-status.ps1`, and service JSON logs. Do not paste `docker inspect`, generated files, DSNs, tokens, proxy targets, or credentials into tickets. Preserve failed containers until ownership and failure category are understood.

Normal lifecycle commands never delete persistent volumes. Permanent removal is a separate destructive decision: take and verify a backup, run graceful shutdown, verify no owned Runners remain, use `docker compose down --volumes --remove-orphans`, and securely remove generated state and off-host copies according to retention policy.

## Release verification

Run `scripts/compose-ci.ps1`. It validates SQL generation, Go/Rust tests and policy, all Compose profiles, two repeated failure/security harness passes, deterministic cleanup, multi-architecture images, OCI SBOM/provenance, image metadata secret scans, and HIGH/CRITICAL vulnerability gates.
