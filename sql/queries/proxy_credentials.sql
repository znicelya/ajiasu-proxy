-- name: GetProxyCredential :one
SELECT * FROM endpoints.proxy_credentials WHERE tenant_id=$1 AND id=$2;

-- name: ListProxyCredentials :many
SELECT * FROM endpoints.proxy_credentials WHERE tenant_id=$1 AND endpoint_id=$2 ORDER BY created_at,id;

-- name: GetActiveProxyCredentialByPublicIdentifier :one
SELECT * FROM endpoints.proxy_credentials WHERE tenant_id=$1 AND public_identifier=$2 AND revoked_at IS NULL;
