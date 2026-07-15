-- name: GetUsageWindow :one
SELECT * FROM gateways.usage_windows WHERE tenant_id=$1 AND endpoint_id=$2 AND credential_id=$3 AND window_start=$4;
