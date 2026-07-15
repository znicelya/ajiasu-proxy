-- name: GetUsageWindow :one
SELECT * FROM gateways.usage_windows WHERE tenant_id=$1 AND endpoint_id=$2 AND credential_id=$3 AND window_start=$4;

-- name: UpsertUsageWindow :exec
INSERT INTO gateways.usage_windows (tenant_id, endpoint_id, credential_id, window_start, window_seconds, active_connections, connection_count, bytes_in, bytes_out, version, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10)
ON CONFLICT (tenant_id, endpoint_id, credential_id, window_start) DO UPDATE SET
  active_connections = gateways.usage_windows.active_connections + EXCLUDED.active_connections,
  connection_count = gateways.usage_windows.connection_count + EXCLUDED.connection_count,
  bytes_in = gateways.usage_windows.bytes_in + EXCLUDED.bytes_in,
  bytes_out = gateways.usage_windows.bytes_out + EXCLUDED.bytes_out,
  version = gateways.usage_windows.version + 1,
  updated_at = EXCLUDED.updated_at;
