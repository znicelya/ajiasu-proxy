-- +goose Up
CREATE SCHEMA accounts;
CREATE SCHEMA pools;

REVOKE ALL ON SCHEMA accounts, pools FROM PUBLIC;
GRANT USAGE ON SCHEMA accounts, pools TO ajiasu_app, ajiasu_platform;

CREATE TABLE tenancy.tenant_quotas (
    tenant_id uuid PRIMARY KEY REFERENCES tenancy.tenants(id) ON DELETE CASCADE,
    max_accounts integer NOT NULL DEFAULT 100 CHECK (max_accounts BETWEEN 1 AND 1000),
    max_pools integer NOT NULL DEFAULT 50 CHECK (max_pools BETWEEN 1 AND 500),
    max_pool_memberships integer NOT NULL DEFAULT 1000 CHECK (max_pool_memberships BETWEEN 1 AND 10000),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

INSERT INTO tenancy.tenant_quotas (tenant_id, created_at, updated_at)
SELECT id, now(), now() FROM tenancy.tenants;

CREATE TABLE accounts.accounts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenancy.tenants(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    normalized_name text NOT NULL CHECK (normalized_name = lower(btrim(normalized_name)) AND char_length(normalized_name) BETWEEN 1 AND 200),
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'disabled', 'deleting')),
    health text NOT NULL DEFAULT 'unknown' CHECK (health IN ('unknown', 'healthy', 'degraded', 'unhealthy', 'quarantined')),
    membership_id text CHECK (membership_id IS NULL OR char_length(btrim(membership_id)) BETWEEN 1 AND 512),
    membership_expires_at timestamptz,
    labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
    max_concurrency integer NOT NULL DEFAULT 1 CHECK (max_concurrency BETWEEN 1 AND 32),
    health_successes integer NOT NULL DEFAULT 0 CHECK (health_successes >= 0),
    health_failures integer NOT NULL DEFAULT 0 CHECK (health_failures >= 0),
    last_health_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, normalized_name),
    UNIQUE (tenant_id, id)
);
CREATE INDEX accounts_tenant_created_id_idx ON accounts.accounts (tenant_id, created_at, id);
CREATE INDEX accounts_tenant_state_health_idx ON accounts.accounts (tenant_id, state, health);

CREATE TABLE accounts.account_credentials (
    tenant_id uuid NOT NULL,
    account_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    provider text NOT NULL CHECK (provider IN ('envelope', 'vault', 'kms')),
    key_id text NOT NULL DEFAULT '' CHECK (char_length(key_id) <= 1024),
    ciphertext bytea NOT NULL DEFAULT ''::bytea,
    wrapped_dek bytea NOT NULL DEFAULT ''::bytea,
    external_ref text NOT NULL DEFAULT '' CHECK (char_length(external_ref) <= 4096),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    retired_at timestamptz,
    PRIMARY KEY (tenant_id, account_id, version),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts.accounts (tenant_id, id) ON DELETE RESTRICT,
    CHECK (
      (provider = 'envelope' AND octet_length(ciphertext) > 0 AND octet_length(wrapped_dek) > 0 AND external_ref = '') OR
      (provider IN ('vault', 'kms') AND external_ref <> '' AND octet_length(ciphertext) = 0 AND octet_length(wrapped_dek) = 0)
    )
);
CREATE UNIQUE INDEX account_credentials_one_active_idx
    ON accounts.account_credentials (tenant_id, account_id) WHERE retired_at IS NULL;

CREATE TABLE accounts.account_capacity_reservations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    account_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts.accounts (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, account_id, owner_id),
    UNIQUE (tenant_id, id)
);
CREATE INDEX account_reservations_active_idx
    ON accounts.account_capacity_reservations (tenant_id, account_id, expires_at);

CREATE TABLE pools.account_pools (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenancy.tenants(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    normalized_name text NOT NULL CHECK (normalized_name = lower(btrim(normalized_name)) AND char_length(normalized_name) BETWEEN 1 AND 200),
    strategy text NOT NULL DEFAULT 'least_connections' CHECK (strategy IN ('least_connections', 'round_robin', 'fixed_priority')),
    selector jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(selector) = 'object'),
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'disabled', 'deleting')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, normalized_name),
    UNIQUE (tenant_id, id)
);
CREATE INDEX account_pools_tenant_created_id_idx ON pools.account_pools (tenant_id, created_at, id);

CREATE TABLE pools.account_pool_memberships (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    pool_id uuid NOT NULL,
    account_id uuid NOT NULL,
    priority integer NOT NULL DEFAULT 100 CHECK (priority BETWEEN 0 AND 1000),
    weight integer NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 100),
    enabled boolean NOT NULL DEFAULT true,
    expires_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (tenant_id, pool_id) REFERENCES pools.account_pools (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts.accounts (tenant_id, id) ON DELETE RESTRICT,
    UNIQUE (tenant_id, pool_id, account_id),
    UNIQUE (tenant_id, pool_id, id)
);
CREATE INDEX pool_memberships_pool_created_id_idx ON pools.account_pool_memberships (tenant_id, pool_id, created_at, id);

ALTER TABLE tenancy.tenant_quotas ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenancy.tenant_quotas FORCE ROW LEVEL SECURITY;
ALTER TABLE accounts.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE accounts.account_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.account_credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE accounts.account_capacity_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.account_capacity_reservations FORCE ROW LEVEL SECURITY;
ALTER TABLE pools.account_pools ENABLE ROW LEVEL SECURITY;
ALTER TABLE pools.account_pools FORCE ROW LEVEL SECURITY;
ALTER TABLE pools.account_pool_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE pools.account_pool_memberships FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_quotas_app_all ON tenancy.tenant_quotas FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id())
  WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY tenant_quotas_platform_insert ON tenancy.tenant_quotas FOR INSERT TO ajiasu_platform WITH CHECK (true);

CREATE POLICY accounts_app_all ON accounts.accounts FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY account_credentials_app_all ON accounts.account_credentials FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY account_reservations_app_all ON accounts.account_capacity_reservations FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY account_pools_app_all ON pools.account_pools FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY pool_memberships_app_all ON pools.account_pool_memberships FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());

REVOKE ALL ON tenancy.tenant_quotas, accounts.accounts, accounts.account_credentials,
  accounts.account_capacity_reservations, pools.account_pools, pools.account_pool_memberships
  FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT, INSERT, UPDATE ON tenancy.tenant_quotas TO ajiasu_app;
GRANT INSERT ON tenancy.tenant_quotas TO ajiasu_platform;
GRANT SELECT, INSERT, UPDATE ON accounts.accounts TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE ON accounts.account_credentials TO ajiasu_app;
GRANT SELECT, INSERT, DELETE ON accounts.account_capacity_reservations TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE ON pools.account_pools TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON pools.account_pool_memberships TO ajiasu_app;

-- +goose Down
DROP TABLE pools.account_pool_memberships;
DROP TABLE pools.account_pools;
DROP TABLE accounts.account_capacity_reservations;
DROP TABLE accounts.account_credentials;
DROP TABLE accounts.accounts;
DROP TABLE tenancy.tenant_quotas;
DROP SCHEMA pools;
DROP SCHEMA accounts;
