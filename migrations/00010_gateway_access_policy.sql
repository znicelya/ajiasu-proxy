-- +goose Up
CREATE SCHEMA gateways;

REVOKE ALL ON SCHEMA gateways FROM PUBLIC;
GRANT USAGE ON SCHEMA gateways TO ajiasu_platform;
GRANT USAGE ON SCHEMA gateways TO ajiasu_app;

CREATE TABLE endpoints.access_profiles (
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    protocols text[] NOT NULL DEFAULT ARRAY['http']::text[],
    dns_mode text NOT NULL DEFAULT 'gateway' CHECK (dns_mode IN ('gateway','runner')),
    source_cidrs jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(source_cidrs) = 'array'),
    target_allow_cidrs jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_allow_cidrs) = 'array'),
    target_deny_cidrs jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_deny_cidrs) = 'array'),
    target_allow_domains jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_allow_domains) = 'array'),
    target_deny_domains jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_deny_domains) = 'array'),
    allowed_ports jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_ports) = 'array'),
    policy_document jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(policy_document) = 'object'),
    policy_hash text NOT NULL DEFAULT '' CHECK (char_length(policy_hash) <= 128),
    max_connections integer NOT NULL DEFAULT 100 CHECK (max_connections BETWEEN 1 AND 100000),
    max_connection_rate integer NOT NULL DEFAULT 50 CHECK (max_connection_rate BETWEEN 1 AND 100000),
    idle_timeout_seconds integer NOT NULL DEFAULT 300 CHECK (idle_timeout_seconds BETWEEN 1 AND 86400),
    max_bytes_per_connection bigint NOT NULL DEFAULT 0 CHECK (max_bytes_per_connection >= 0),
    traffic_window_seconds integer NOT NULL DEFAULT 0 CHECK (traffic_window_seconds BETWEEN 0 AND 86400),
    max_window_bytes bigint NOT NULL DEFAULT 0 CHECK (max_window_bytes >= 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, endpoint_id),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE,
    CHECK (cardinality(protocols) > 0),
    CHECK (protocols <@ ARRAY['http','connect','socks5']::text[]),
    CHECK ((traffic_window_seconds = 0 AND max_window_bytes = 0) OR (traffic_window_seconds > 0 AND max_window_bytes > 0))
);

CREATE TABLE endpoints.proxy_credentials (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    public_identifier text NOT NULL CHECK (char_length(public_identifier) BETWEEN 8 AND 128),
    verifier text NOT NULL CHECK (char_length(verifier) BETWEEN 32 AND 1024),
    expires_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, public_identifier)
);
CREATE INDEX proxy_credentials_active_idx ON endpoints.proxy_credentials (tenant_id, endpoint_id, public_identifier) WHERE revoked_at IS NULL;

CREATE TABLE gateways.gateways (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    normalized_name text NOT NULL UNIQUE CHECK (normalized_name = lower(btrim(normalized_name))),
    certificate_fingerprint text NOT NULL UNIQUE CHECK (char_length(certificate_fingerprint) BETWEEN 32 AND 256),
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','disabled')),
    connectivity_state text NOT NULL DEFAULT 'registering' CHECK (connectivity_state IN ('registering','online','stale','offline')),
    gateway_version text NOT NULL DEFAULT '' CHECK (char_length(gateway_version) <= 128),
    architecture text NOT NULL DEFAULT '' CHECK (char_length(architecture) <= 64),
    last_heartbeat_at timestamptz,
    session_generation bigint NOT NULL DEFAULT 1 CHECK (session_generation > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX gateways_connectivity_idx ON gateways.gateways (connectivity_state, state, created_at, id);

CREATE TABLE gateways.enrollments (
    id uuid PRIMARY KEY,
    expected_gateway_name text NOT NULL CHECK (char_length(btrim(expected_gateway_name)) BETWEEN 1 AND 200),
    token_prefix text NOT NULL CHECK (char_length(token_prefix) = 12),
    token_verifier text NOT NULL CHECK (char_length(token_verifier) >= 32),
    certificate_fingerprint text NOT NULL CHECK (char_length(certificate_fingerprint) BETWEEN 32 AND 256),
    created_by uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    consumed_gateway_id uuid REFERENCES gateways.gateways(id) ON DELETE RESTRICT,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_gateway_id IS NOT NULL)
);
CREATE UNIQUE INDEX gateway_enrollments_active_prefix_idx ON gateways.enrollments (token_prefix) WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE TABLE gateways.sessions (
    id uuid PRIMARY KEY,
    gateway_id uuid NOT NULL REFERENCES gateways.gateways(id) ON DELETE CASCADE,
    gateway_instance_id uuid NOT NULL,
    token_prefix text NOT NULL CHECK (char_length(token_prefix) = 12),
    token_verifier text NOT NULL CHECK (char_length(token_verifier) >= 32),
    protocol_revision integer NOT NULL CHECK (protocol_revision = 1),
    session_generation bigint NOT NULL CHECK (session_generation > 0),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX gateway_sessions_active_gateway_idx ON gateways.sessions (gateway_id) WHERE revoked_at IS NULL;
CREATE INDEX gateway_sessions_prefix_idx ON gateways.sessions (token_prefix);

CREATE TABLE gateways.route_grants (
    gateway_id uuid NOT NULL REFERENCES gateways.gateways(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    runner_id uuid NOT NULL,
    desired_generation bigint NOT NULL CHECK (desired_generation > 0),
    policy_hash text NOT NULL CHECK (char_length(policy_hash) <= 128),
    expires_at timestamptz NOT NULL,
    signature bytea NOT NULL CHECK (octet_length(signature) BETWEEN 32 AND 256),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (gateway_id, tenant_id, endpoint_id),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX route_grants_expiry_idx ON gateways.route_grants (expires_at, gateway_id);

CREATE TABLE gateways.usage_windows (
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    credential_id uuid,
    window_start timestamptz NOT NULL,
    window_seconds integer NOT NULL CHECK (window_seconds > 0),
    active_connections integer NOT NULL DEFAULT 0 CHECK (active_connections >= 0),
    connection_count bigint NOT NULL DEFAULT 0 CHECK (connection_count >= 0),
    bytes_in bigint NOT NULL DEFAULT 0 CHECK (bytes_in >= 0),
    bytes_out bigint NOT NULL DEFAULT 0 CHECK (bytes_out >= 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, endpoint_id, credential_id, window_start),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, credential_id) REFERENCES endpoints.proxy_credentials (tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE endpoints.access_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE endpoints.access_profiles FORCE ROW LEVEL SECURITY;
ALTER TABLE endpoints.proxy_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE endpoints.proxy_credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE gateways.gateways ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateways.gateways FORCE ROW LEVEL SECURITY;
ALTER TABLE gateways.enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateways.enrollments FORCE ROW LEVEL SECURITY;
ALTER TABLE gateways.sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateways.sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE gateways.route_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateways.route_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE gateways.usage_windows ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateways.usage_windows FORCE ROW LEVEL SECURITY;

CREATE POLICY access_profiles_app_all ON endpoints.access_profiles FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY access_profiles_platform_all ON endpoints.access_profiles FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY proxy_credentials_app_all ON endpoints.proxy_credentials FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY proxy_credentials_platform_all ON endpoints.proxy_credentials FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY gateways_platform_all ON gateways.gateways FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY gateway_enrollments_platform_all ON gateways.enrollments FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY gateway_sessions_platform_all ON gateways.sessions FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY route_grants_platform_all ON gateways.route_grants FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY usage_windows_platform_all ON gateways.usage_windows FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY usage_windows_app_all ON gateways.usage_windows FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());

REVOKE ALL ON endpoints.access_profiles, endpoints.proxy_credentials,
  gateways.gateways, gateways.enrollments, gateways.sessions,
  gateways.route_grants, gateways.usage_windows FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON endpoints.access_profiles, endpoints.proxy_credentials TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON endpoints.access_profiles, endpoints.proxy_credentials,
  gateways.gateways, gateways.enrollments, gateways.sessions,
  gateways.route_grants, gateways.usage_windows TO ajiasu_platform;
GRANT SELECT, INSERT, UPDATE ON gateways.usage_windows TO ajiasu_app;

-- +goose Down
DROP POLICY usage_windows_app_all ON gateways.usage_windows;
DROP POLICY usage_windows_platform_all ON gateways.usage_windows;
DROP POLICY route_grants_platform_all ON gateways.route_grants;
DROP POLICY gateway_sessions_platform_all ON gateways.sessions;
DROP POLICY gateway_enrollments_platform_all ON gateways.enrollments;
DROP POLICY gateways_platform_all ON gateways.gateways;
DROP POLICY proxy_credentials_platform_all ON endpoints.proxy_credentials;
DROP POLICY proxy_credentials_app_all ON endpoints.proxy_credentials;
DROP POLICY access_profiles_platform_all ON endpoints.access_profiles;
DROP POLICY access_profiles_app_all ON endpoints.access_profiles;
DROP TABLE gateways.usage_windows;
DROP TABLE gateways.route_grants;
DROP TABLE gateways.sessions;
DROP TABLE gateways.enrollments;
DROP TABLE gateways.gateways;
DROP TABLE endpoints.proxy_credentials;
DROP TABLE endpoints.access_profiles;
DROP SCHEMA gateways;
