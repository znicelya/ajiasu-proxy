-- +goose Up
CREATE TABLE platform.idempotency_records (
    id uuid PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('platform', 'tenant')),
    tenant_id uuid REFERENCES tenancy.tenants (id) ON DELETE RESTRICT,
    actor_id uuid NOT NULL,
    method text NOT NULL CHECK (method ~ '^[A-Z]+$' AND char_length(method) <= 16),
    canonical_route text NOT NULL CHECK (canonical_route LIKE '/api/v1/%' AND char_length(canonical_route) <= 512),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    response_status integer CHECK (response_status BETWEEN 200 AND 599),
    response_body bytea,
    response_protected boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    expires_at timestamptz NOT NULL,
    CHECK ((scope = 'platform' AND tenant_id IS NULL) OR
           (scope = 'tenant' AND tenant_id IS NOT NULL)),
    CHECK (expires_at > created_at),
    CHECK ((response_status IS NULL AND response_body IS NULL AND response_protected = false AND completed_at IS NULL) OR
           (response_status IS NOT NULL AND response_body IS NOT NULL AND completed_at IS NOT NULL)),
    UNIQUE NULLS NOT DISTINCT (scope, tenant_id, actor_id, method, canonical_route, idempotency_key)
);
CREATE INDEX idempotency_records_expiry_idx ON platform.idempotency_records (expires_at, id);
CREATE INDEX idempotency_records_tenant_actor_idx
    ON platform.idempotency_records (tenant_id, actor_id, created_at DESC, id)
    WHERE scope = 'tenant';

ALTER TABLE platform.idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.idempotency_records FORCE ROW LEVEL SECURITY;

CREATE POLICY idempotency_records_app_all ON platform.idempotency_records FOR ALL TO ajiasu_app
    USING (scope = 'tenant' AND tenant_id = platform.current_tenant_id())
    WITH CHECK (scope = 'tenant' AND platform.current_tenant_id() IS NOT NULL AND tenant_id = platform.current_tenant_id());
CREATE POLICY idempotency_records_platform_all ON platform.idempotency_records FOR ALL TO ajiasu_platform
    USING (true) WITH CHECK (true);

REVOKE ALL ON platform.idempotency_records FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON platform.idempotency_records TO ajiasu_app, ajiasu_platform;

-- +goose Down
DROP TABLE platform.idempotency_records;
