-- name: CountAccounts :one
SELECT count(*) FROM accounts.accounts WHERE tenant_id = $1;

-- name: GetAccount :one
SELECT * FROM accounts.accounts WHERE tenant_id = $1 AND id = $2;

-- name: ListAccounts :many
SELECT * FROM accounts.accounts
WHERE tenant_id = $1 AND (created_at, id) > ($2, $3)
ORDER BY created_at, id LIMIT $4;

-- name: GetActiveCredential :one
SELECT * FROM accounts.account_credentials
WHERE tenant_id = $1 AND account_id = $2 AND retired_at IS NULL;

-- name: CountActiveReservations :one
SELECT count(*) FROM accounts.account_capacity_reservations
WHERE tenant_id = $1 AND account_id = $2 AND expires_at > $3;

