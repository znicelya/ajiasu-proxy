-- name: InsertAuditAndOutbox :exec
WITH inserted_audit AS (
INSERT INTO audit.audit_events (
    id,
    tenant_id,
    actor_type,
    actor_id,
    action,
    resource_type,
    resource_id,
    result,
    source_ip,
    user_agent,
    request_id,
    details,
    created_at
) VALUES (
    sqlc.arg(audit_id),
    sqlc.narg(audit_tenant_id),
    sqlc.arg(actor_type),
    sqlc.narg(actor_id),
    sqlc.arg(action),
    sqlc.arg(resource_type),
    sqlc.narg(resource_id),
    sqlc.arg(result),
    sqlc.arg(source_ip),
    sqlc.arg(user_agent),
    sqlc.arg(request_id),
    sqlc.arg(details),
    sqlc.arg(audit_created_at)
)
RETURNING id
)
INSERT INTO platform.outbox_events (
    id,
    tenant_id,
    event_type,
    aggregate_type,
    aggregate_id,
    payload_version,
    payload,
    created_at,
    available_at
)
SELECT sqlc.arg(outbox_id),
       sqlc.narg(outbox_tenant_id),
       sqlc.arg(event_type),
       sqlc.arg(aggregate_type),
       sqlc.arg(aggregate_id),
       sqlc.arg(payload_version),
       sqlc.arg(payload),
       sqlc.arg(outbox_created_at),
       sqlc.arg(available_at)
FROM inserted_audit;

-- name: LeaseOutboxEvents :many
WITH candidates AS (
    SELECT id
    FROM platform.outbox_events
    WHERE processed_at IS NULL
      AND available_at <= sqlc.arg(lease_now)::timestamptz
      AND (lease_deadline IS NULL OR lease_deadline <= sqlc.arg(lease_now)::timestamptz)
    ORDER BY available_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_limit)::integer
), leased AS (
    UPDATE platform.outbox_events AS event
    SET lease_owner = sqlc.arg(owner_id),
        lease_deadline = sqlc.arg(lease_deadline),
        attempts = event.attempts + 1
    FROM candidates
    WHERE event.id = candidates.id
    RETURNING event.id,
              event.tenant_id,
              event.event_type,
              event.aggregate_type,
              event.aggregate_id,
              event.payload_version,
              event.payload,
              event.created_at,
              event.available_at,
              event.lease_owner,
              event.lease_deadline,
              event.attempts,
              event.processed_at
)
SELECT id,
       tenant_id,
       event_type,
       aggregate_type,
       aggregate_id,
       payload_version,
       payload,
       created_at,
       available_at,
       lease_owner,
       lease_deadline,
       attempts,
       processed_at
FROM leased
ORDER BY available_at, created_at, id;

-- name: CompleteOutboxEvent :execrows
UPDATE platform.outbox_events
SET processed_at = sqlc.arg(processed_at),
    lease_owner = NULL,
    lease_deadline = NULL
WHERE id = sqlc.arg(id)
  AND lease_owner = sqlc.arg(owner_id)
  AND processed_at IS NULL;

-- name: ReleaseOutboxEvent :execrows
UPDATE platform.outbox_events
SET available_at = sqlc.arg(available_at),
    lease_owner = NULL,
    lease_deadline = NULL
WHERE id = sqlc.arg(id)
  AND lease_owner = sqlc.arg(owner_id)
  AND processed_at IS NULL;
