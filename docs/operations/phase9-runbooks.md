# Phase 9 Operator Runbooks

## Readiness

Confirm `/readyz`, PostgreSQL schema 11, Redis reachability, current Agent and
Gateway sessions, and route snapshot freshness. Do not restart every component
simultaneously. Preserve logs and operation IDs before intervention.

## HTTP errors

Compare 5xx rate with dependency health and migration state. Never attach
authorization headers, cookies, request bodies, proxy credentials, or arbitrary
upstream errors to tickets. Roll back only after checking schema compatibility.

## Lease contention

Inspect scheduler replicas, Redis latency, lease namespace, fencing failures,
and PostgreSQL committed assignments. Do not delete leases manually. Reduce
new allocation pressure and allow bounded reconciliation to converge.

## Redis

Redis loss blocks unsafe new pooled allocations but must not erase committed
assignments. Restore the dependency, verify fencing health, and wait for
debounce and rebuild. Never restore Redis as authoritative business state.

## Backup and restore

Follow `phase9-recovery-capacity.md`. The critical set is PostgreSQL plus the
matching keyring. Run the restore evidence script and fixed/pool smoke probes.
RPO must be <= 15 minutes and RTO <= 60 minutes.

## Security incident

Revoke sessions and enrollment material, rotate affected service identities,
preserve immutable audit records, and treat Agent compromise as node/runtime
compromise. Rotate the keyring only through the documented re-encryption plan.

## Release verification

Download release artifacts to an empty directory and run
`scripts/phase9-release-verify.ps1`. Verify image signatures against the
GitHub Actions OIDC identity recorded in `build/release-policy.yaml`, compare
digests with the manifest, and archive the verification result.
