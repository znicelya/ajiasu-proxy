-- name: GetOperation :one
SELECT * FROM operations.operations WHERE id=$1 AND (tenant_id=$2 OR ($2::uuid IS NULL AND tenant_id IS NULL));

-- name: ListTenantOperations :many
SELECT * FROM operations.operations WHERE tenant_id=$1 AND (created_at,id)<($2,$3)
ORDER BY created_at DESC,id DESC LIMIT $4;

