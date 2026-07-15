# Phase 2 Control Plane Operations

The Phase 2 control plane provides the tenant, identity, RBAC, audit, session, service-token, idempotency, and management API foundation. It is not yet the complete proxy product and is not described as production-ready before the later Compose and Helm phases.

## Development startup

1. Start PostgreSQL 17 and Keycloak 26 using the digest-pinned images in `build/control-plane-images.lock`.
2. Create separate login roles for the normal and platform DSNs. The normal login must inherit only `ajiasu_app`; the platform login must inherit only `ajiasu_platform`. Neither login may be superuser or have `BYPASSRLS`.
3. Apply all migrations with the repository-pinned Goose tool:

   ```powershell
   go tool goose -dir migrations postgres $env:AJIASU_MIGRATION_DSN up
   ```

4. Mount a 32-byte keyring file and a non-empty OIDC client-secret file as read-only files.
5. Set the required environment variables and run:

   ```powershell
   go run ./cmd/control-plane
   ```

The process can start before migrations are applied. `/readyz` remains `503` with a redacted `not_ready` response until the database roles and exactly the supported Phase 2 schema version are available. The running process rechecks readiness and installs the management API without requiring a restart.

## Required configuration

The complete field list and validation rules live in `internal/platform/config/config.go`. Required categories are:

- `AJIASU_ENVIRONMENT`: `development` or `production`.
- HTTP bind and explicit read-header, read, write, idle, and shutdown timeouts.
- Separate normal and platform PostgreSQL DSNs plus pool limits.
- OIDC issuer, client ID, client-secret file, and callback URL.
- Session cookie name, secure flag, idle timeout, and absolute timeout.
- `AJIASU_KEYRING_FILE`: an exact 32-byte regular file. On Unix it must not be accessible by group or other users.
- Local break-glass enablement and allowed source CIDRs.

Production rejects insecure session cookies and non-HTTPS OIDC URLs. DSNs, key material, passwords, OIDC credentials, session values, and service tokens are never printed by configuration logging.

## Local break-glass bootstrap

The local administrator is disabled by default. Enable it only with an explicit source-CIDR allowlist. Bootstrap interactively so secrets do not enter command history or process arguments:

```powershell
go run ./cmd/control-plane admin bootstrap
```

The command reads the password and TOTP secret from the terminal, encrypts the TOTP secret through the deployment keyring, and returns recovery codes once. Store recovery codes in an approved secret manager. Losing both TOTP and recovery material requires an audited database recovery procedure; do not create an unaudited replacement row.

## OIDC and Keycloak testing

The integration realm is `internal/testkit/testdata/keycloak/ajiasu-test-realm.json`. Tests start the digest-pinned Keycloak image and verify discovery, PKCE, nonce, signature, audience, expiry, JIT identity creation, and key rotation behavior. A first OIDC login creates a global identity and session but grants no tenant membership or role.

Discovery or JWKS failure blocks new OIDC logins. Existing valid database sessions continue to authenticate as long as PostgreSQL remains available and the identity has not been disabled.

## Health behavior

- `/livez` reports process liveness only and does not query dependencies.
- `/readyz` checks both PostgreSQL pools and requires schema version 7. Dependency and schema errors are redacted from the HTTP response.
- PostgreSQL loss makes readiness fail and management requests return stable dependency errors where safe.
- An unsupported older or newer schema keeps the service unready.

Do not route management traffic to an instance until `/readyz` returns `200`.

## Migrations and rollback rehearsal

All migrations contain reviewed up and down sections. CI exercises a fresh up, down to the previous version, and up again. Before applying a migration:

1. Take and verify a PostgreSQL backup.
2. Confirm the application version supports the target migration range.
3. Apply migrations with the dedicated migration credential, not either runtime credential.
4. Wait for every replica to report ready.
5. Run the Phase 2 API and isolation smoke tests.

Downgrading the schema while a Phase 2 instance is running intentionally makes readiness fail until the supported schema is restored.

## Backup expectations

PostgreSQL is authoritative for tenants, memberships, role bindings, identities, sessions, idempotency results, audit events, and outbox state. Backups must therefore include every non-template database and the Goose version table. Use encrypted backups, test restores regularly, and target an RPO of at most 15 minutes and an RTO of at most 60 minutes. Keyring and OIDC client-secret files require a separate encrypted backup and rotation process; they are not stored in PostgreSQL.

Audit and idempotency retention jobs must be explicit and reviewed. Never truncate audit data as an incidental cleanup step.

## Image runtime contract

`Dockerfile.control-plane` builds a static amd64/arm64 binary with the digest-pinned Go image and runs it from a digest-pinned Alpine image as numeric UID/GID 65532. The binary is root-owned mode `0555`, the working directory is empty, and the application is compatible with a read-only root filesystem. Mount secret files read-only and provide no Docker socket, Linux capabilities, or writable host paths.

## Explicit Phase 2 exclusions

Phase 2 does not implement AJiaSu account inventory or pools, scheduling, node agents, Runner lifecycle orchestration, proxy gateways, Redis coordination, the web console, Kubernetes workload identity, Agent mTLS, Compose packaging, or Helm packaging. Those remain later roadmap phases.
