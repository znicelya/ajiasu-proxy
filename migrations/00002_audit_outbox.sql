-- +goose Up
CREATE TABLE audit.audit_events (
    id uuid PRIMARY KEY,
    tenant_id uuid,
    actor_type text NOT NULL CHECK (char_length(btrim(actor_type)) BETWEEN 1 AND 64),
    actor_id uuid,
    action text NOT NULL CHECK (char_length(btrim(action)) BETWEEN 1 AND 128),
    resource_type text NOT NULL CHECK (char_length(btrim(resource_type)) BETWEEN 1 AND 64),
    resource_id uuid,
    result text NOT NULL CHECK (char_length(btrim(result)) BETWEEN 1 AND 64),
    source_ip inet NOT NULL,
    user_agent text NOT NULL CHECK (char_length(btrim(user_agent)) BETWEEN 1 AND 1024),
    request_id uuid NOT NULL,
    details jsonb NOT NULL CHECK (jsonb_typeof(details) = 'object'),
    created_at timestamptz NOT NULL
);

CREATE INDEX audit_events_tenant_created_id_idx
    ON audit.audit_events (tenant_id, created_at DESC, id DESC);
CREATE INDEX audit_events_request_id_idx
    ON audit.audit_events (request_id);

ALTER TABLE audit.audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.audit_events FORCE ROW LEVEL SECURITY;

CREATE POLICY audit_events_tenant_select
    ON audit.audit_events
    FOR SELECT
    TO ajiasu_app
    USING (tenant_id IS NOT NULL AND tenant_id = platform.current_tenant_id());

CREATE POLICY audit_events_tenant_insert
    ON audit.audit_events
    FOR INSERT
    TO ajiasu_app
    WITH CHECK (tenant_id IS NOT NULL AND tenant_id = platform.current_tenant_id());

-- Mutation policies do not grant UPDATE or DELETE. They ensure the append-only
-- trigger remains the final defense if a future privilege grant is mistaken.
CREATE POLICY audit_events_tenant_update_guard
    ON audit.audit_events
    FOR UPDATE
    TO ajiasu_app
    USING (tenant_id IS NOT NULL AND tenant_id = platform.current_tenant_id())
    WITH CHECK (tenant_id IS NOT NULL AND tenant_id = platform.current_tenant_id());

CREATE POLICY audit_events_tenant_delete_guard
    ON audit.audit_events
    FOR DELETE
    TO ajiasu_app
    USING (tenant_id IS NOT NULL AND tenant_id = platform.current_tenant_id());

CREATE POLICY audit_events_platform_select
    ON audit.audit_events
    FOR SELECT
    TO ajiasu_platform
    USING (true);

CREATE POLICY audit_events_platform_insert
    ON audit.audit_events
    FOR INSERT
    TO ajiasu_platform
    WITH CHECK (true);

CREATE POLICY audit_events_platform_update_guard
    ON audit.audit_events
    FOR UPDATE
    TO ajiasu_platform
    USING (true)
    WITH CHECK (true);

CREATE POLICY audit_events_platform_delete_guard
    ON audit.audit_events
    FOR DELETE
    TO ajiasu_platform
    USING (true);

REVOKE ALL ON audit.audit_events FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT, INSERT ON audit.audit_events TO ajiasu_app, ajiasu_platform;

-- +goose StatementBegin
CREATE FUNCTION audit.reject_audit_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
  RAISE EXCEPTION 'audit events are append-only' USING ERRCODE = '55000';
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION audit.reject_audit_mutation() FROM PUBLIC, ajiasu_app, ajiasu_platform;

CREATE TRIGGER audit_events_append_only
    BEFORE UPDATE OR DELETE ON audit.audit_events
    FOR EACH ROW
    EXECUTE FUNCTION audit.reject_audit_mutation();

CREATE TABLE platform.outbox_events (
    id uuid PRIMARY KEY,
    tenant_id uuid,
    event_type text NOT NULL CHECK (char_length(btrim(event_type)) BETWEEN 1 AND 128),
    aggregate_type text NOT NULL CHECK (char_length(btrim(aggregate_type)) BETWEEN 1 AND 64),
    aggregate_id uuid NOT NULL,
    payload_version integer NOT NULL CHECK (payload_version > 0),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    lease_owner uuid,
    lease_deadline timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    processed_at timestamptz,
    CHECK ((lease_owner IS NULL) = (lease_deadline IS NULL)),
    CHECK (processed_at IS NULL OR (lease_owner IS NULL AND lease_deadline IS NULL))
);

CREATE INDEX outbox_events_ready_idx
    ON platform.outbox_events (available_at, created_at, id)
    WHERE processed_at IS NULL;
CREATE INDEX outbox_events_lease_deadline_idx
    ON platform.outbox_events (lease_deadline)
    WHERE processed_at IS NULL AND lease_deadline IS NOT NULL;

ALTER TABLE platform.outbox_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.outbox_events FORCE ROW LEVEL SECURITY;

CREATE POLICY outbox_events_tenant_insert
    ON platform.outbox_events
    FOR INSERT
    TO ajiasu_app
    WITH CHECK (tenant_id IS NOT NULL AND tenant_id = platform.current_tenant_id());

CREATE POLICY outbox_events_platform_all
    ON platform.outbox_events
    FOR ALL
    TO ajiasu_platform
    USING (true)
    WITH CHECK (true);

REVOKE ALL ON platform.outbox_events FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT INSERT ON platform.outbox_events TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE ON platform.outbox_events TO ajiasu_platform;

-- +goose Down
DROP TABLE platform.outbox_events;

DROP TRIGGER audit_events_append_only ON audit.audit_events;
DROP TABLE audit.audit_events;
DROP FUNCTION audit.reject_audit_mutation();
