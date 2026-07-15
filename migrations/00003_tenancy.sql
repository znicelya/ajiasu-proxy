-- +goose Up
CREATE TABLE tenancy.tenants (
    id uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE CHECK (slug = lower(slug) AND char_length(slug) BETWEEN 1 AND 63),
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    state text NOT NULL CHECK (state IN ('active', 'suspended', 'deleting')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX tenants_state_created_id_idx ON tenancy.tenants (state, created_at, id);

CREATE TABLE tenancy.memberships (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenancy.tenants(id) ON DELETE RESTRICT,
    identity_id uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, identity_id),
    UNIQUE (tenant_id, id)
);
CREATE INDEX memberships_identity_tenant_idx ON tenancy.memberships (identity_id, tenant_id);
CREATE INDEX memberships_tenant_created_id_idx ON tenancy.memberships (tenant_id, created_at, id);

CREATE TABLE tenancy.role_bindings (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    membership_id uuid NOT NULL,
    role text NOT NULL CHECK (role IN ('tenant_admin', 'operator', 'auditor', 'consumer')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (tenant_id, membership_id) REFERENCES tenancy.memberships (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (membership_id, role)
);
CREATE INDEX role_bindings_membership_idx ON tenancy.role_bindings (membership_id);
CREATE INDEX role_bindings_tenant_role_membership_idx ON tenancy.role_bindings (tenant_id, role, membership_id);

ALTER TABLE tenancy.tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenancy.tenants FORCE ROW LEVEL SECURITY;
ALTER TABLE tenancy.memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenancy.memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE tenancy.role_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenancy.role_bindings FORCE ROW LEVEL SECURITY;

CREATE POLICY tenants_app_select ON tenancy.tenants FOR SELECT TO ajiasu_app
    USING (id = platform.current_tenant_id());
CREATE POLICY tenants_platform_select ON tenancy.tenants FOR SELECT TO ajiasu_platform USING (true);
CREATE POLICY tenants_platform_insert ON tenancy.tenants FOR INSERT TO ajiasu_platform WITH CHECK (true);
CREATE POLICY tenants_platform_update ON tenancy.tenants FOR UPDATE TO ajiasu_platform USING (true) WITH CHECK (true);

CREATE POLICY memberships_app_all ON tenancy.memberships FOR ALL TO ajiasu_app
    USING (tenant_id = platform.current_tenant_id())
    WITH CHECK (platform.current_tenant_id() IS NOT NULL AND tenant_id = platform.current_tenant_id());
CREATE POLICY memberships_platform_select ON tenancy.memberships FOR SELECT TO ajiasu_platform USING (true);
CREATE POLICY memberships_platform_insert ON tenancy.memberships FOR INSERT TO ajiasu_platform WITH CHECK (true);
CREATE POLICY role_bindings_app_all ON tenancy.role_bindings FOR ALL TO ajiasu_app
    USING (tenant_id = platform.current_tenant_id())
    WITH CHECK (platform.current_tenant_id() IS NOT NULL AND tenant_id = platform.current_tenant_id());
CREATE POLICY role_bindings_platform_select ON tenancy.role_bindings FOR SELECT TO ajiasu_platform USING (true);
CREATE POLICY role_bindings_platform_insert ON tenancy.role_bindings FOR INSERT TO ajiasu_platform WITH CHECK (true);

REVOKE ALL ON tenancy.tenants, tenancy.memberships, tenancy.role_bindings FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT ON tenancy.tenants TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE ON tenancy.tenants TO ajiasu_platform;
GRANT SELECT, INSERT, DELETE ON tenancy.memberships, tenancy.role_bindings TO ajiasu_app;
GRANT SELECT, INSERT ON tenancy.memberships, tenancy.role_bindings TO ajiasu_platform;

-- +goose Down
DROP TABLE tenancy.role_bindings;
DROP TABLE tenancy.memberships;
DROP TABLE tenancy.tenants;
