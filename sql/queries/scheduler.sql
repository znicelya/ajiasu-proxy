-- name: GetEndpointAssignment :one
SELECT * FROM scheduler.endpoint_assignments WHERE tenant_id=$1 AND endpoint_id=$2;

-- name: ListAssignmentsByState :many
SELECT * FROM scheduler.endpoint_assignments WHERE state=$1 ORDER BY updated_at,tenant_id,endpoint_id LIMIT $2;

-- name: UpdateAssignmentStateWithFence :one
UPDATE scheduler.endpoint_assignments SET state=$4,health_state=$5,retry_attempts=$6,cooldown_until=$7,valid_until=$8,last_reason_code=$9,fencing_token=$3,updated_at=$10
WHERE tenant_id=$1 AND endpoint_id=$2 AND fencing_token <= $3
RETURNING *;

-- name: GetHealthObservation :one
SELECT * FROM scheduler.health_observations WHERE tenant_id=$1 AND resource_type=$2 AND resource_id=$3 AND dimension=$4;

-- name: ListHealthByState :many
SELECT * FROM scheduler.health_observations WHERE state=$1 ORDER BY updated_at,tenant_id,resource_type,resource_id LIMIT $2;

-- name: GetPoolCursor :one
SELECT * FROM scheduler.pool_cursors WHERE tenant_id=$1 AND pool_id=$2;

-- name: AdvancePoolCursorWithFence :one
INSERT INTO scheduler.pool_cursors (tenant_id,pool_id,cursor,fencing_token,version,updated_at) VALUES ($1,$2,$3,$4,1,$5)
ON CONFLICT (tenant_id,pool_id) DO UPDATE SET cursor=EXCLUDED.cursor,fencing_token=EXCLUDED.fencing_token,version=scheduler.pool_cursors.version+1,updated_at=EXCLUDED.updated_at
WHERE scheduler.pool_cursors.fencing_token <= EXCLUDED.fencing_token
RETURNING *;

-- name: ListMigrationAttempts :many
SELECT * FROM scheduler.migration_attempts WHERE tenant_id=$1 AND endpoint_id=$2 ORDER BY created_at,id;
