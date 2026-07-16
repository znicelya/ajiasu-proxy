-- +goose Up
CREATE SCHEMA scheduler;

REVOKE ALL ON SCHEMA scheduler FROM PUBLIC;
GRANT USAGE ON SCHEMA scheduler TO ajiasu_app, ajiasu_platform;

ALTER TABLE endpoints.proxy_endpoints
    DROP CONSTRAINT proxy_endpoints_binding_mode_check,
    ALTER COLUMN account_id DROP NOT NULL,
    ALTER COLUMN node_id DROP NOT NULL,
    ADD COLUMN pool_id uuid;

ALTER TABLE endpoints.proxy_endpoints
    ADD CONSTRAINT proxy_endpoints_pool_fk
      FOREIGN KEY (tenant_id, pool_id) REFERENCES pools.account_pools (tenant_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT proxy_endpoints_binding_check CHECK (
      (binding_mode = 'fixed' AND account_id IS NOT NULL AND node_id IS NOT NULL AND pool_id IS NULL)
      OR
      (binding_mode = 'pool' AND account_id IS NULL AND node_id IS NULL AND pool_id IS NOT NULL)
    );

CREATE INDEX proxy_endpoints_pool_state_idx
    ON endpoints.proxy_endpoints (tenant_id, pool_id, lifecycle_state, desired_runner_state)
    WHERE binding_mode = 'pool';

CREATE TABLE scheduler.endpoint_assignments (
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    assignment_id uuid NOT NULL,
    pool_id uuid,
    account_id uuid,
    node_id uuid,
    runner_id uuid,
    capacity_reservation_id uuid,
    desired_generation bigint NOT NULL CHECK (desired_generation > 0),
    fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    strategy text NOT NULL DEFAULT '' CHECK (strategy IN ('','least_connections','round_robin','fixed_priority')),
    state text NOT NULL DEFAULT 'unassigned' CHECK (state IN ('unassigned','acquiring','assigned','draining','migrating','releasing','blocked','failed')),
    health_state text NOT NULL DEFAULT 'unknown' CHECK (health_state IN ('unknown','healthy','degraded','unhealthy','quarantined')),
    retry_attempts integer NOT NULL DEFAULT 0 CHECK (retry_attempts BETWEEN 0 AND 1000),
    cooldown_until timestamptz,
    valid_until timestamptz,
    last_reason_code text NOT NULL DEFAULT '' CHECK (char_length(last_reason_code) <= 128),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, endpoint_id),
    UNIQUE (assignment_id),
    UNIQUE (tenant_id, assignment_id),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, pool_id) REFERENCES pools.account_pools (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts.accounts (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (node_id) REFERENCES nodes.nodes (id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, capacity_reservation_id) REFERENCES accounts.account_capacity_reservations (tenant_id, id) ON DELETE RESTRICT,
    CHECK ((state IN ('assigned','draining','migrating')) = (account_id IS NOT NULL AND node_id IS NOT NULL AND runner_id IS NOT NULL)),
    CHECK (pool_id IS NOT NULL OR strategy = '')
);
CREATE INDEX endpoint_assignments_state_cooldown_idx ON scheduler.endpoint_assignments (state, cooldown_until, updated_at);
CREATE INDEX endpoint_assignments_account_idx ON scheduler.endpoint_assignments (tenant_id, account_id, state) WHERE account_id IS NOT NULL;
CREATE INDEX endpoint_assignments_node_idx ON scheduler.endpoint_assignments (node_id, state) WHERE node_id IS NOT NULL;

CREATE TABLE scheduler.health_observations (
    tenant_id uuid NOT NULL,
    resource_type text NOT NULL CHECK (resource_type IN ('account','endpoint','runner','assignment')),
    resource_id uuid NOT NULL,
    dimension text NOT NULL CHECK (dimension IN ('process','tunnel','egress','account')),
    state text NOT NULL DEFAULT 'unknown' CHECK (state IN ('unknown','healthy','degraded','unhealthy','quarantined')),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
    consecutive_successes integer NOT NULL DEFAULT 0 CHECK (consecutive_successes >= 0),
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    reason_code text NOT NULL DEFAULT '' CHECK (char_length(reason_code) <= 128),
    cooldown_until timestamptz,
    last_observed_at timestamptz,
    last_transition_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, resource_type, resource_id, dimension),
    FOREIGN KEY (tenant_id) REFERENCES tenancy.tenants(id) ON DELETE CASCADE
);
CREATE INDEX health_observations_state_cooldown_idx ON scheduler.health_observations (state, cooldown_until, updated_at);

CREATE TABLE scheduler.pool_cursors (
    tenant_id uuid NOT NULL,
    pool_id uuid NOT NULL,
    cursor bigint NOT NULL DEFAULT 0 CHECK (cursor >= 0),
    fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, pool_id),
    FOREIGN KEY (tenant_id, pool_id) REFERENCES pools.account_pools (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE scheduler.migration_attempts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    assignment_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    migration_type text NOT NULL CHECK (migration_type IN ('runner_rebuild','account_replacement','node_migration')),
    source_account_id uuid,
    source_node_id uuid,
    target_account_id uuid,
    target_node_id uuid,
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    attempt integer NOT NULL CHECK (attempt BETWEEN 1 AND 1000),
    state text NOT NULL CHECK (state IN ('queued','running','succeeded','failed','cancelled')),
    result_code text NOT NULL DEFAULT '' CHECK (char_length(result_code) <= 128),
    cooldown_until timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, endpoint_id, migration_type, attempt),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, assignment_id) REFERENCES scheduler.endpoint_assignments (tenant_id, assignment_id) ON DELETE CASCADE,
    FOREIGN KEY (operation_id) REFERENCES operations.operations (id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, source_account_id) REFERENCES accounts.accounts (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (source_node_id) REFERENCES nodes.nodes (id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, target_account_id) REFERENCES accounts.accounts (tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (target_node_id) REFERENCES nodes.nodes (id) ON DELETE RESTRICT
);
CREATE INDEX migration_attempts_state_cooldown_idx ON scheduler.migration_attempts (state, cooldown_until, created_at, id);

ALTER TABLE scheduler.endpoint_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduler.endpoint_assignments FORCE ROW LEVEL SECURITY;
ALTER TABLE scheduler.health_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduler.health_observations FORCE ROW LEVEL SECURITY;
ALTER TABLE scheduler.pool_cursors ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduler.pool_cursors FORCE ROW LEVEL SECURITY;
ALTER TABLE scheduler.migration_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduler.migration_attempts FORCE ROW LEVEL SECURITY;

CREATE POLICY endpoint_assignments_app_select ON scheduler.endpoint_assignments FOR SELECT TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id());
CREATE POLICY endpoint_assignments_app_insert ON scheduler.endpoint_assignments FOR INSERT TO ajiasu_app
  WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY endpoint_assignments_platform_all ON scheduler.endpoint_assignments FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY health_observations_app_select ON scheduler.health_observations FOR SELECT TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id());
CREATE POLICY health_observations_platform_all ON scheduler.health_observations FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY pool_cursors_platform_all ON scheduler.pool_cursors FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY migration_attempts_app_select ON scheduler.migration_attempts FOR SELECT TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id());
CREATE POLICY migration_attempts_platform_all ON scheduler.migration_attempts FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);

REVOKE ALL ON scheduler.endpoint_assignments, scheduler.health_observations, scheduler.pool_cursors, scheduler.migration_attempts FROM PUBLIC;
GRANT SELECT, INSERT ON scheduler.endpoint_assignments TO ajiasu_app;
GRANT SELECT ON scheduler.health_observations, scheduler.migration_attempts TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON scheduler.endpoint_assignments, scheduler.health_observations, scheduler.pool_cursors, scheduler.migration_attempts TO ajiasu_platform;

-- +goose Down
DROP POLICY migration_attempts_platform_all ON scheduler.migration_attempts;
DROP POLICY migration_attempts_app_select ON scheduler.migration_attempts;
DROP POLICY pool_cursors_platform_all ON scheduler.pool_cursors;
DROP POLICY health_observations_platform_all ON scheduler.health_observations;
DROP POLICY health_observations_app_select ON scheduler.health_observations;
DROP POLICY endpoint_assignments_platform_all ON scheduler.endpoint_assignments;
DROP POLICY endpoint_assignments_app_insert ON scheduler.endpoint_assignments;
DROP POLICY endpoint_assignments_app_select ON scheduler.endpoint_assignments;

DROP TABLE scheduler.migration_attempts;
DROP TABLE scheduler.pool_cursors;
DROP TABLE scheduler.health_observations;
DROP TABLE scheduler.endpoint_assignments;

DROP INDEX endpoints.proxy_endpoints_pool_state_idx;
ALTER TABLE endpoints.proxy_endpoints
    DROP CONSTRAINT proxy_endpoints_binding_check,
    DROP CONSTRAINT proxy_endpoints_pool_fk,
    DROP COLUMN pool_id,
    ALTER COLUMN account_id SET NOT NULL,
    ALTER COLUMN node_id SET NOT NULL,
    ADD CONSTRAINT proxy_endpoints_binding_mode_check CHECK (binding_mode = 'fixed');

DROP SCHEMA scheduler;
