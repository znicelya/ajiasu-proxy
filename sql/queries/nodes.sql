-- name: GetNode :one
SELECT * FROM nodes.nodes WHERE id = $1;

-- name: ListNodes :many
SELECT * FROM nodes.nodes WHERE (created_at,id) > ($1,$2) ORDER BY created_at,id LIMIT $3;

-- name: GetEnrollmentByPrefix :one
SELECT * FROM nodes.node_enrollments WHERE token_prefix = $1 AND consumed_at IS NULL AND revoked_at IS NULL;

-- name: GetSessionByPrefix :one
SELECT * FROM nodes.node_sessions WHERE token_prefix = $1 AND revoked_at IS NULL;

