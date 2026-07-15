-- name: CountPools :one
SELECT count(*) FROM pools.account_pools WHERE tenant_id = $1;

-- name: GetAccountPool :one
SELECT * FROM pools.account_pools WHERE tenant_id = $1 AND id = $2;

-- name: ListAccountPools :many
SELECT * FROM pools.account_pools
WHERE tenant_id = $1 AND (created_at, id) > ($2, $3)
ORDER BY created_at, id LIMIT $4;

-- name: ListPoolMemberships :many
SELECT * FROM pools.account_pool_memberships
WHERE tenant_id = $1 AND pool_id = $2 AND (created_at, id) > ($3, $4)
ORDER BY created_at, id LIMIT $5;

