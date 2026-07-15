-- name: GetEndpoint :one
SELECT e.*, s.observed_generation, s.observed_state, s.runner_id, s.reason_code,
       s.last_agent_observation_at, s.last_transition_at
FROM endpoints.proxy_endpoints e
JOIN endpoints.endpoint_status s ON s.tenant_id=e.tenant_id AND s.endpoint_id=e.id
WHERE e.tenant_id=$1 AND e.id=$2;

-- name: ListEndpoints :many
SELECT e.*, s.observed_generation, s.observed_state, s.runner_id, s.reason_code,
       s.last_agent_observation_at, s.last_transition_at
FROM endpoints.proxy_endpoints e
JOIN endpoints.endpoint_status s ON s.tenant_id=e.tenant_id AND s.endpoint_id=e.id
WHERE e.tenant_id=$1 AND (e.created_at,e.id)>($2,$3)
ORDER BY e.created_at,e.id LIMIT $4;

