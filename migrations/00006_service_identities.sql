-- +goose Up
CREATE TABLE identity.service_identities (
    id uuid PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('platform', 'tenant')),
    tenant_id uuid REFERENCES tenancy.tenants (id) ON DELETE RESTRICT,
    scope_key uuid GENERATED ALWAYS AS (COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid)) STORED,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    disabled_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK ((scope = 'platform' AND tenant_id IS NULL) OR
           (scope = 'tenant' AND tenant_id IS NOT NULL)),
    UNIQUE (id, scope, scope_key),
    UNIQUE NULLS NOT DISTINCT (scope, tenant_id, name)
);
CREATE INDEX service_identities_tenant_created_id_idx
    ON identity.service_identities (tenant_id, created_at, id)
    WHERE scope = 'tenant';
CREATE INDEX service_identities_platform_created_id_idx
    ON identity.service_identities (created_at, id)
    WHERE scope = 'platform';

CREATE TABLE identity.service_tokens (
    id uuid PRIMARY KEY,
    service_identity_id uuid NOT NULL,
    scope text NOT NULL CHECK (scope IN ('platform', 'tenant')),
    tenant_id uuid,
    scope_key uuid GENERATED ALWAYS AS (COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid)) STORED,
    prefix text NOT NULL CHECK (prefix ~ '^[A-Za-z0-9_-]{12}$'),
    verifier text NOT NULL CHECK (char_length(verifier) BETWEEN 32 AND 1024),
    role text NOT NULL CHECK (role IN ('platform_admin', 'tenant_admin', 'operator', 'auditor', 'consumer')),
    source_cidr cidr,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    CHECK ((scope = 'platform' AND tenant_id IS NULL AND role = 'platform_admin') OR
           (scope = 'tenant' AND tenant_id IS NOT NULL AND role <> 'platform_admin')),
    CHECK (expires_at > created_at),
    CHECK (expires_at <= created_at + interval '24 hours'),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    FOREIGN KEY (service_identity_id, scope, scope_key)
        REFERENCES identity.service_identities (id, scope, scope_key) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id) REFERENCES tenancy.tenants (id) ON DELETE RESTRICT
);
CREATE INDEX service_tokens_active_prefix_scope_idx
    ON identity.service_tokens (prefix, scope, tenant_id, expires_at, id)
    WHERE revoked_at IS NULL;
CREATE INDEX service_tokens_prefix_scope_idx
    ON identity.service_tokens (prefix, scope, tenant_id, created_at DESC, id);
CREATE INDEX service_tokens_identity_active_idx
    ON identity.service_tokens (service_identity_id, expires_at, id)
    WHERE revoked_at IS NULL;

ALTER TABLE identity.service_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.service_identities FORCE ROW LEVEL SECURITY;
ALTER TABLE identity.service_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.service_tokens FORCE ROW LEVEL SECURITY;

CREATE POLICY service_identities_app_all ON identity.service_identities FOR ALL TO ajiasu_app
    USING (scope = 'tenant' AND tenant_id = platform.current_tenant_id())
    WITH CHECK (scope = 'tenant' AND platform.current_tenant_id() IS NOT NULL AND tenant_id = platform.current_tenant_id());
CREATE POLICY service_identities_platform_all ON identity.service_identities FOR ALL TO ajiasu_platform
    USING (true) WITH CHECK (true);
CREATE POLICY service_tokens_app_all ON identity.service_tokens FOR ALL TO ajiasu_app
    USING (scope = 'tenant' AND tenant_id = platform.current_tenant_id())
    WITH CHECK (scope = 'tenant' AND platform.current_tenant_id() IS NOT NULL AND tenant_id = platform.current_tenant_id());
CREATE POLICY service_tokens_platform_all ON identity.service_tokens FOR ALL TO ajiasu_platform
    USING (true) WITH CHECK (true);

REVOKE ALL ON identity.service_identities, identity.service_tokens FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON identity.service_identities, identity.service_tokens TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON identity.service_identities, identity.service_tokens TO ajiasu_platform;

-- +goose Down
DROP TABLE identity.service_tokens;
DROP TABLE identity.service_identities;
