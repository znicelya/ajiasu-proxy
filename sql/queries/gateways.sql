-- name: GetGateway :one
SELECT * FROM gateways.gateways WHERE id=$1;

-- name: ListGateways :many
SELECT * FROM gateways.gateways ORDER BY created_at,id;
