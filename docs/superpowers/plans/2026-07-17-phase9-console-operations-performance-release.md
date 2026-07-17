# Phase 9 Console, Operations, Performance, and Release Hardening Plan

## Execution rules

- Each task below is one independently reviewable commit.
- Write or update a failing test before each behavior change.
- Never add credentials, real proxy targets, raw audit payloads, or generated
  secrets to fixtures or CI artifacts.
- Keep the existing `/api/v1` contracts and compatibility matrix authoritative.

## Task 1: Build the React operator console

Create the `console/` application, Fluent UI theme, typed API client,
authentication/session handling, route shell, and pages for all management
resources. Add tenant-aware route guards, safe mutations with `If-Match` /
`Idempotency-Key`, redacted error rendering, pagination, and accessibility
tests. Add a build gate and a static fixture mode for CI.

## Task 2: Add observability, dashboards, and SIEM export contracts

Add `deploy/observability/` Prometheus rules, Grafana dashboard JSON,
OpenTelemetry configuration, and audit export schema/examples. Add tests that
reject unbounded metric labels and secret-bearing dashboard queries.

## Task 3: Add recovery rehearsal and 10k-connection load suite

Add `scripts/phase9-restore-rehearsal.ps1`, evidence schema/fixture, and
documented PITR/keyring checks. Add a Go load harness with bounded connection
concurrency, fake upstream, resource sampling hooks, and invariant checks for
leases and account capacity. Keep the full 10,000-connection run protected
behind an explicit environment gate.

## Task 4: Harden releases and publish operator runbooks

Add signing/provenance/SBOM CI workflow contracts, compatibility matrix,
release-note template, and runbooks. Add static checks for immutable digests,
signature verification commands, SBOM presence, and redaction. Record Phase 9
exit evidence and known environment-gated tests.

## Final verification

Run console build/accessibility tests, observability contract tests,
restore-rehearsal dry-run, protected load gate, release metadata checks, and
the full existing Go/Rust/Compose/Helm test suites. Commit each task
separately and report every environment-gated check explicitly.
