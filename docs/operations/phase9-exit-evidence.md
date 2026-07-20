# Phase 9 Exit Evidence

## Implemented evidence

- React operator Console with tenant-scoped navigation, typed API access,
  redacted rendering, pagination, optimistic-concurrency headers, responsive
  layout, reduced-motion handling, visible keyboard focus, and a skip link.
- Prometheus recording and alert rules, Grafana dashboard, OpenTelemetry
  collector configuration, and redacted SIEM audit export contracts.
- PostgreSQL PITR/keyring recovery guidance, machine-readable restore evidence,
  and a bounded load harness with an explicit 10,000-connection safety gate.
- Immutable-image, SBOM, provenance, signature, compatibility, release-note,
  and operator-runbook contracts.

## Locally verified on 2026-07-20

- `npm ci`: completed from `console/package-lock.json` after removing the
  incomplete dependency installation.
- `npm run build` in `console/`: passed (`tsc --noEmit` and Vite production
  build).
- `tests/console/contract.ps1`: passed, including the Console accessibility
  contract.
- `tests/observability/contract.ps1`: passed.
- `tests/recovery/contract.ps1`: passed with fixture RPO 10 minutes and RTO
  30 minutes.
- `tests/release/contract.ps1`: passed.
- `go test` for Phase 9 and non-container contract, integration, failure, and
  security packages: passed.
- `cargo test --workspace --all-features --locked`: passed.
- `git diff --check`: passed before recording this evidence.

## Environment-gated evidence

The protected 10,000-connection run was not executed on this workstation.
It requires `AJIASU_PHASE9_LOAD_GATE=I_UNDERSTAND`, a disposable target using
fake AJiaSu credentials, external resource monitoring, and before/after
queries proving that leases and account reservations were not oversold. A
unit test verifies the harness and the explicit safety gate, but it is not a
substitute for measured capacity evidence.

The restore contract uses synthetic timestamps and fixture verification. A
production-like PostgreSQL PITR restore with the matching keyring, schema and
credential verification, empty Redis reconstruction, and fixed/pool smoke
probes remains required before signing the recovery exit criterion.

The full parallel and serial `go test ./...` runs did not complete because
Testcontainers calls stalled against Docker Desktop while creating or
terminating PostgreSQL containers. The bounded rerun identified timeouts in
container-backed migration, audit, testkit, and tenant-isolation tests. The
non-container Go packages and Phase 9-specific packages passed.

`tests/compose/run.ps1 -Repeat 1` also timed out while exercising the same
Docker-backed path. Docker itself responded to `docker version`, so this is
recorded as an unresolved local Testcontainers/Compose environment gate rather
than a passing result.

Helm is not installed on this workstation. Consequently the rendered Helm
security gate and disposable Kind cluster tests must run in
`.github/workflows/helm-ci.yml` or on a prepared operator host.

Phase 9 implementation is complete, but the phase exit gate must remain open
until the real restore rehearsal, protected 10,000-connection capacity run,
Docker-backed full Go/Compose suite, and Helm/Kind suite produce passing
evidence.
