# Control Plane Phase 3 Operations

Phase 3 adds tenant-scoped AJiaSu accounts, encrypted credential versions, account pools, quotas, and transactional capacity reservations. It does not start AJiaSu processes, schedule nodes, publish proxy endpoints, or return plaintext credentials from management APIs.

## Secret provider

The control plane uses the deployment keyring file to construct the default `envelope` provider. Each credential version receives a random 256-bit data-encryption key; the payload is encrypted with AES-256-GCM and the data key is wrapped by the deployment keyring. Tenant ID, account ID, credential version, and purpose are authenticated at both layers.

Back up the keyring independently from PostgreSQL. A database backup without its matching key cannot recover credentials. Never copy encrypted credential rows between accounts or tenants. Restore drills must prove the original key opens a test credential and a different key does not.

Vault and KMS adapters implement the same provider contract through narrow external-client interfaces. Backend references, ciphertext, wrapped keys, usernames, and passwords must not be written to application logs, audit details, Outbox payloads, metrics labels, or support tickets.

## Quotas

Every tenant has one quota row. Defaults are 100 accounts, 50 pools, and 1000 pool memberships. Tenant administrators can read and update limits within the hard ranges exposed by the API. A limit cannot be reduced below current usage.

Quota checks take the tenant advisory lock and the quota row lock in the same transaction as resource creation. Do not bypass services with manual inserts. Bulk imports accept 1–100 items, validate the complete request first, and return safe ordered per-item results.

## Account lifecycle and rotation

Accounts start `active`, with health `unknown` and concurrency `1` unless explicitly set. Credential rotation appends a new version and retires the previous active version atomically. GET and list routes expose only provider/version timestamps.

Use `POST /api/v1/tenants/{tenant_id}/accounts/{account_id}/credentials/rotate` for planned or incident rotation. Verify the returned version increased, then inspect audit event `accounts.credential.rotated`. There is intentionally no credential export endpoint.

## Pools and capacity

Pool membership is explicit. If a pool has an exact-match selector, an account must match all selector labels before it can be added. Capacity includes active, enabled, non-expired members whose accounts are not unhealthy or quarantined. Active, non-expired reservations consume one concurrency unit each.

Phase 3 reservations are a database coordination primitive for Phase 4. They are not a scheduler. Reservation owners must release reservations when work ends; expired reservations no longer consume capacity.

## Migration and rollback

Schema version 8 is required. Rehearse `7 -> 8 -> 7 -> 8` against a disposable restored database before production rollout. Rolling back migration 8 drops all Phase 3 data, so production rollback requires an explicit data-loss decision and a verified backup.

After migration, check `/readyz`, create a fake account, rotate its fake credential, build a matching pool, and verify capacity. Scan HTTP output, logs, audit, Outbox, idempotency rows, and metrics for the fake credential canary before declaring the rollout complete.
