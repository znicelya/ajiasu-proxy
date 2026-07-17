# Phase 9 Recovery and Capacity

## PostgreSQL PITR and keyring

Production PostgreSQL must archive WAL continuously to encrypted off-host
storage. A base backup without its WAL range is not a PITR backup. Retain the
Control Plane keyring under separate access control and record its checksum and
key identifier in backup metadata. A database restore without the matching
keyring is not successful because encrypted account credentials remain
unusable.

Restore into an empty environment, replay WAL to the selected recovery point,
verify schema 11, verify an encrypted fixture with the restored keyring, leave
Redis empty, and allow scheduler leases and route caches to reconstruct. Run
fixed and pooled proxy smoke probes before accepting the evidence.

Use `scripts/phase9-restore-rehearsal.ps1` to calculate and enforce RPO <= 15
minutes and RTO <= 60 minutes. Store evidence separately from database dumps
and secrets.

## Capacity gate

`go run ./cmd/phase9-load` opens bounded HTTP CONNECT sessions and reports
successes, failures, p95/p99 establishment latency, client heap, and duration.
Pull-request tests use a local fake server. The 10,000-connection run requires
`AJIASU_PHASE9_LOAD_GATE=I_UNDERSTAND`, a disposable environment, fake AJiaSu
credentials, and external monitoring of Gateway, Agent, Redis, PostgreSQL, and
Control Plane memory.

The run fails if configured error or heap limits are exceeded. Operators must
also query committed assignments and account reservations before and after the
run to prove no lease oversell or account-limit violation.
