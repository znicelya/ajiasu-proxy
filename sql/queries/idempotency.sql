-- name: ReserveIdempotencyRecord :one
INSERT INTO platform.idempotency_records (
    id, scope, tenant_id, actor_id, method, canonical_route, idempotency_key,
    request_hash, response_status, response_body, response_protected, created_at, completed_at, expires_at
) VALUES (
    sqlc.arg(id), sqlc.arg(scope), sqlc.narg(tenant_id), sqlc.arg(actor_id),
    sqlc.arg(method), sqlc.arg(canonical_route), sqlc.arg(idempotency_key),
    sqlc.arg(request_hash), NULL, NULL, false, sqlc.arg(created_at), NULL, sqlc.arg(expires_at)
)
ON CONFLICT (scope, tenant_id, actor_id, method, canonical_route, idempotency_key)
DO NOTHING
RETURNING *;

-- name: GetIdempotencyRecordForUpdate :one
SELECT * FROM platform.idempotency_records
WHERE scope = sqlc.arg(scope)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND actor_id = sqlc.arg(actor_id)
  AND method = sqlc.arg(method)
  AND canonical_route = sqlc.arg(canonical_route)
  AND idempotency_key = sqlc.arg(idempotency_key)
FOR UPDATE;

-- name: CompleteIdempotencyRecord :one
UPDATE platform.idempotency_records
SET response_status = sqlc.arg(response_status),
    response_body = sqlc.arg(response_body),
    response_protected = sqlc.arg(response_protected),
    completed_at = sqlc.arg(completed_at)
WHERE id = sqlc.arg(id) AND response_status IS NULL
RETURNING *;

-- name: DeleteExpiredIdempotencyRecords :execrows
DELETE FROM platform.idempotency_records WHERE expires_at <= sqlc.arg(now);
