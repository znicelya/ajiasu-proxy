-- name: GetAccessProfile :one
SELECT * FROM endpoints.access_profiles WHERE tenant_id=$1 AND endpoint_id=$2;
