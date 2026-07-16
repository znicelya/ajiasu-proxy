# Phase 7 Compose gate harness

Run `./tests/compose/run.ps1` for the deterministic default gate. It repeats scheduler, isolation, failure, fake Runner protocol, packaged Agent/Gateway, lifecycle, recovery, Docker model, and security inspection tests. Every Docker resource created by the harness carries a unique `ajiasu.test_run` label and is removed by exact label in `finally`.

`-Full` additionally invokes the real single-host lifecycle. It requires an environment ID beginning with `phase7-e2e-` and a smoke JSON document containing `"fixture_only": true` plus `fixed` and `pool` fake proxy probes. This guard prevents the suite from using protected real-account credentials.

The checked-in previous-release fixture exercises immutable rollback metadata. Backup payloads and credentials are generated only in temporary directories and are never committed.
