-- name: GetRunnerDesiredState :one
SELECT * FROM reconciler.runner_desired_states WHERE tenant_id=$1 AND endpoint_id=$2;

-- name: LeaseWorkItems :many
WITH candidates AS (
  SELECT id FROM reconciler.work_items
  WHERE available_at <= $1 AND (lease_owner IS NULL OR lease_deadline <= $1)
  ORDER BY available_at,created_at,id
  FOR UPDATE SKIP LOCKED LIMIT $2
)
UPDATE reconciler.work_items w SET lease_owner=$3,lease_deadline=$4,attempts=w.attempts+1,updated_at=$1
FROM candidates c WHERE w.id=c.id RETURNING w.*;

