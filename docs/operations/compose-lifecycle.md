# Docker Compose lifecycle operations

Run all commands from the repository checkout and use the same environment file and mode that were passed to `compose-init.ps1`.

## Start and readiness

`compose-up.ps1` validates Docker and the rendered release, starts local dependencies when selected, runs the migration job once, waits for Control Plane readiness, enrolls Agent and Gateway only when their one-time files are absent, and waits for both sessions to become healthy. Production acceptance should pass a private smoke JSON file containing `fixed` and `pool` objects with `proxy_uri`, `target_uri`, `username`, `password`, and optional `expected_status`. `-SkipSmoke` is an explicit development escape hatch.

Failures are non-destructive: containers, logs, state volumes, and generated material remain available for diagnosis. Re-running the command is safe because migrations and service starts are idempotent and key material is never rotated.

## Bounded status

`compose-status.ps1` returns one compact JSON document. It reports only bounded component states and aggregate node, Gateway, session, and fixed/pool assignment counts. It never prints DSNs, enrollment tokens, session tokens, proxy credentials, targets, or container environment values.

## Drain and shutdown

`compose-down.ps1` marks nodes and active assignments as draining before it stops Gateway listeners. It then stops Agent, identifies Runner containers by the exact `ajiasu.owner=control-plane` and current `ajiasu.node_id` labels, validates every ownership label, and removes only those verified Runners. Malformed ownership, timeout, or cleanup failure stops the workflow and preserves remaining containers and volumes.

Dependencies stop last unless `-KeepDependencies` is used. Persistent volumes are never deleted by lifecycle commands.
