# Compose backup, restore, upgrade, and rollback

`compose-backup.ps1` creates a PostgreSQL custom-format dump, a checksum manifest, CA context, and a separately located keyring artifact. Redis, leases, sessions, Runner state, and route caches are intentionally excluded. Production operators must copy the database dump and keyring to separately encrypted off-host retention. The target is an RPO no greater than 15 minutes.

Restore is destructive and accepts only `-Disposable`. It verifies environment identity, schema 11, artifact sizes and SHA-256 checksums before stopping anything, recreates only the PostgreSQL volume, restores the matching keyring, and refuses an incompatible schema. Do not use it against external managed databases; use provider PITR and then apply the same manifest/keyring checks.

Upgrade requires immutable target images, reads the target Control Plane compatibility metadata, creates and verifies a pre-upgrade backup, drains the current stack, migrates through the normal one-shot path, and accepts the release only after component/session readiness and fixed/pool smoke probes.

Rollback always restores the pre-upgrade database and matching keyring before starting the previous manifest. Changing image tags without validating the database path is not rollback. A failed restore, migration, readiness check, or smoke probe preserves the remaining diagnostic state and exits nonzero.

For the single-host rehearsal, record start/end timestamps in the incident log. The Phase 7 objective is RPO <= 15 minutes and RTO <= 60 minutes; measured values belong in the release evidence, not hard-coded into scripts.
