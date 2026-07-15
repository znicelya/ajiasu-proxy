-- name: CurrentContext :one
SELECT platform.current_tenant_id() AS tenant_id,
       platform.current_actor_id() AS actor_id;
