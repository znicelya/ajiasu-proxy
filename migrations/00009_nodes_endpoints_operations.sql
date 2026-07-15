-- +goose Up
CREATE SCHEMA nodes;
CREATE SCHEMA endpoints;
CREATE SCHEMA operations;
CREATE SCHEMA reconciler;

REVOKE ALL ON SCHEMA nodes, endpoints, operations, reconciler FROM PUBLIC;
GRANT USAGE ON SCHEMA nodes, endpoints, operations, reconciler TO ajiasu_app, ajiasu_platform;

CREATE TABLE nodes.nodes (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    normalized_name text NOT NULL UNIQUE CHECK (normalized_name = lower(btrim(normalized_name)) AND char_length(normalized_name) BETWEEN 1 AND 200),
    desired_labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(desired_labels) = 'object'),
    observed_labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(observed_labels) = 'object'),
    max_runners integer NOT NULL DEFAULT 10 CHECK (max_runners BETWEEN 1 AND 1000),
    reserved_headroom integer NOT NULL DEFAULT 1 CHECK (reserved_headroom BETWEEN 0 AND 999 AND reserved_headroom < max_runners),
    active_runners integer NOT NULL DEFAULT 0 CHECK (active_runners BETWEEN 0 AND 1000),
    maintenance_state text NOT NULL DEFAULT 'active' CHECK (maintenance_state IN ('active','cordoned','draining','disabled')),
    connectivity_state text NOT NULL DEFAULT 'registering' CHECK (connectivity_state IN ('registering','online','stale','offline')),
    architecture text NOT NULL DEFAULT '' CHECK (char_length(architecture) <= 64),
    agent_version text NOT NULL DEFAULT '' CHECK (char_length(agent_version) <= 128),
    runtime_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(runtime_capabilities) = 'array'),
    last_heartbeat_at timestamptz,
    session_generation bigint NOT NULL DEFAULT 1 CHECK (session_generation > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX nodes_connectivity_maintenance_idx ON nodes.nodes (connectivity_state, maintenance_state, created_at, id);

CREATE TABLE nodes.node_enrollments (
    id uuid PRIMARY KEY,
    expected_node_name text NOT NULL CHECK (char_length(btrim(expected_node_name)) BETWEEN 1 AND 200),
    token_prefix text NOT NULL CHECK (char_length(token_prefix) = 12),
    token_verifier text NOT NULL CHECK (char_length(token_verifier) >= 32),
    created_by uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    consumed_node_id uuid REFERENCES nodes.nodes(id) ON DELETE RESTRICT,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_node_id IS NOT NULL),
    CHECK (consumed_at IS NULL OR revoked_at IS NULL)
);
CREATE UNIQUE INDEX node_enrollments_active_prefix_idx ON nodes.node_enrollments (token_prefix) WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE TABLE nodes.node_sessions (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes.nodes(id) ON DELETE CASCADE,
    agent_instance_id uuid NOT NULL,
    token_prefix text NOT NULL CHECK (char_length(token_prefix) = 12),
    token_verifier text NOT NULL CHECK (char_length(token_verifier) >= 32),
    protocol_revision integer NOT NULL CHECK (protocol_revision IN (1,2)),
    session_generation bigint NOT NULL CHECK (session_generation > 0),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX node_sessions_active_node_idx ON nodes.node_sessions (node_id) WHERE revoked_at IS NULL;
CREATE INDEX node_sessions_prefix_idx ON nodes.node_sessions (token_prefix);

CREATE TABLE endpoints.proxy_endpoints (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenancy.tenants(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    normalized_name text NOT NULL CHECK (normalized_name = lower(btrim(normalized_name)) AND char_length(normalized_name) BETWEEN 1 AND 200),
    binding_mode text NOT NULL DEFAULT 'fixed' CHECK (binding_mode = 'fixed'),
    account_id uuid NOT NULL,
    node_id uuid NOT NULL REFERENCES nodes.nodes(id) ON DELETE RESTRICT,
    desired_runner_state text NOT NULL DEFAULT 'running' CHECK (desired_runner_state IN ('running','stopped')),
    lifecycle_state text NOT NULL DEFAULT 'active' CHECK (lifecycle_state IN ('active','disabled','deleting')),
    desired_generation bigint NOT NULL DEFAULT 1 CHECK (desired_generation > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts.accounts (tenant_id, id) ON DELETE RESTRICT,
    UNIQUE (tenant_id, normalized_name),
    UNIQUE (tenant_id, id)
);
CREATE INDEX proxy_endpoints_tenant_created_id_idx ON endpoints.proxy_endpoints (tenant_id, created_at, id);
CREATE INDEX proxy_endpoints_node_state_idx ON endpoints.proxy_endpoints (node_id, lifecycle_state, desired_runner_state);

CREATE TABLE endpoints.endpoint_status (
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    observed_generation bigint NOT NULL DEFAULT 0 CHECK (observed_generation >= 0),
    observed_state text NOT NULL DEFAULT 'pending' CHECK (observed_state IN ('pending','starting','running','stopping','stopped','failed','orphaned')),
    runner_id uuid,
    reason_code text NOT NULL DEFAULT 'awaiting_reconciliation' CHECK (char_length(reason_code) BETWEEN 1 AND 128),
    last_agent_observation_at timestamptz,
    last_transition_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, endpoint_id),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE operations.operations (
    id uuid PRIMARY KEY,
    tenant_id uuid,
    operation_type text NOT NULL CHECK (char_length(operation_type) BETWEEN 1 AND 128),
    resource_type text NOT NULL CHECK (char_length(resource_type) BETWEEN 1 AND 64),
    resource_id uuid NOT NULL,
    requested_generation bigint NOT NULL CHECK (requested_generation > 0),
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','succeeded','failed','cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    progress_category text NOT NULL DEFAULT 'queued' CHECK (char_length(progress_category) BETWEEN 1 AND 64),
    result_code text NOT NULL DEFAULT '' CHECK (char_length(result_code) <= 128),
    safe_message text NOT NULL DEFAULT '' CHECK (char_length(safe_message) <= 512),
    requested_by uuid NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (resource_type, resource_id, requested_generation, operation_type)
);
CREATE INDEX operations_tenant_created_id_idx ON operations.operations (tenant_id, created_at DESC, id DESC);
CREATE INDEX operations_state_created_idx ON operations.operations (state, created_at, id) WHERE state IN ('queued','running');

CREATE TABLE reconciler.runner_desired_states (
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    runner_id uuid NOT NULL,
    node_id uuid NOT NULL REFERENCES nodes.nodes(id) ON DELETE RESTRICT,
    account_id uuid NOT NULL,
    credential_version bigint NOT NULL CHECK (credential_version > 0),
    desired_generation bigint NOT NULL CHECK (desired_generation > 0),
    desired_action text NOT NULL CHECK (desired_action IN ('create','stop','rebuild','garbage_collect')),
    operation_id uuid NOT NULL REFERENCES operations.operations(id) ON DELETE RESTRICT,
    capacity_reservation_id uuid,
    runtime_spec jsonb NOT NULL CHECK (jsonb_typeof(runtime_spec) = 'object'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, endpoint_id),
    UNIQUE (runner_id),
    UNIQUE (operation_id),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id, credential_version) REFERENCES accounts.account_credentials (tenant_id, account_id, version) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, capacity_reservation_id) REFERENCES accounts.account_capacity_reservations (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX runner_desired_node_action_idx ON reconciler.runner_desired_states (node_id, desired_action, updated_at);

CREATE TABLE reconciler.runner_observations (
    runner_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    node_id uuid NOT NULL REFERENCES nodes.nodes(id) ON DELETE RESTRICT,
    operation_id uuid NOT NULL,
    observed_generation bigint NOT NULL CHECK (observed_generation > 0),
    observed_state text NOT NULL CHECK (observed_state IN ('pending','starting','running','stopping','stopped','failed','orphaned')),
    runtime_id text NOT NULL DEFAULT '' CHECK (char_length(runtime_id) <= 256),
    reason_code text NOT NULL CHECK (char_length(reason_code) BETWEEN 1 AND 128),
    restart_count integer NOT NULL DEFAULT 0 CHECK (restart_count >= 0),
    observed_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, endpoint_id),
    FOREIGN KEY (tenant_id, endpoint_id) REFERENCES endpoints.proxy_endpoints (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE reconciler.work_items (
    id uuid PRIMARY KEY,
    tenant_id uuid,
    resource_type text NOT NULL CHECK (char_length(resource_type) BETWEEN 1 AND 64),
    resource_id uuid NOT NULL,
    action text NOT NULL CHECK (char_length(action) BETWEEN 1 AND 64),
    generation bigint NOT NULL CHECK (generation > 0),
    operation_id uuid NOT NULL REFERENCES operations.operations(id) ON DELETE CASCADE,
    available_at timestamptz NOT NULL,
    lease_owner uuid,
    lease_deadline timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_result_code text NOT NULL DEFAULT '' CHECK (char_length(last_result_code) <= 128),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (resource_type, resource_id, action, generation),
    CHECK ((lease_owner IS NULL) = (lease_deadline IS NULL))
);
CREATE INDEX work_items_ready_idx ON reconciler.work_items (available_at, created_at, id) WHERE lease_owner IS NULL;
CREATE INDEX work_items_lease_idx ON reconciler.work_items (lease_deadline) WHERE lease_owner IS NOT NULL;

CREATE TABLE reconciler.finalizers (
    tenant_id uuid NOT NULL,
    resource_type text NOT NULL CHECK (char_length(resource_type) BETWEEN 1 AND 64),
    resource_id uuid NOT NULL,
    finalizer text NOT NULL CHECK (finalizer IN ('runner.cleanup')),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, resource_type, resource_id, finalizer)
);

ALTER TABLE nodes.nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE nodes.nodes FORCE ROW LEVEL SECURITY;
ALTER TABLE nodes.node_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE nodes.node_enrollments FORCE ROW LEVEL SECURITY;
ALTER TABLE nodes.node_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE nodes.node_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE endpoints.proxy_endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE endpoints.proxy_endpoints FORCE ROW LEVEL SECURITY;
ALTER TABLE endpoints.endpoint_status ENABLE ROW LEVEL SECURITY;
ALTER TABLE endpoints.endpoint_status FORCE ROW LEVEL SECURITY;
ALTER TABLE operations.operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.operations FORCE ROW LEVEL SECURITY;
ALTER TABLE reconciler.runner_desired_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE reconciler.runner_desired_states FORCE ROW LEVEL SECURITY;
ALTER TABLE reconciler.runner_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE reconciler.runner_observations FORCE ROW LEVEL SECURITY;
ALTER TABLE reconciler.work_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE reconciler.work_items FORCE ROW LEVEL SECURITY;
ALTER TABLE reconciler.finalizers ENABLE ROW LEVEL SECURITY;
ALTER TABLE reconciler.finalizers FORCE ROW LEVEL SECURITY;

CREATE POLICY nodes_app_select ON nodes.nodes FOR SELECT TO ajiasu_app USING (true);
CREATE POLICY nodes_platform_all ON nodes.nodes FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY node_enrollments_platform_all ON nodes.node_enrollments FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY node_sessions_platform_all ON nodes.node_sessions FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);

CREATE POLICY proxy_endpoints_app_all ON endpoints.proxy_endpoints FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY proxy_endpoints_platform_all ON endpoints.proxy_endpoints FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY endpoint_status_app_all ON endpoints.endpoint_status FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY endpoint_status_platform_all ON endpoints.endpoint_status FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY operations_app_all ON operations.operations FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY operations_platform_all ON operations.operations FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY runner_desired_app_all ON reconciler.runner_desired_states FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY runner_desired_platform_all ON reconciler.runner_desired_states FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY runner_observations_app_all ON reconciler.runner_observations FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY runner_observations_platform_all ON reconciler.runner_observations FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY work_items_app_all ON reconciler.work_items FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY work_items_platform_all ON reconciler.work_items FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY finalizers_app_all ON reconciler.finalizers FOR ALL TO ajiasu_app
  USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id());
CREATE POLICY finalizers_platform_all ON reconciler.finalizers FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);

-- Reconciliation uses the platform database role to converge tenant work while
-- management APIs still enforce explicit tenant grants. These policies expose
-- encrypted credential records only to internal repositories; no HTTP route
-- opens or serializes them.
CREATE POLICY phase4_accounts_platform_select ON accounts.accounts FOR SELECT TO ajiasu_platform USING (true);
CREATE POLICY phase4_credentials_platform_select ON accounts.account_credentials FOR SELECT TO ajiasu_platform USING (true);
CREATE POLICY phase4_reservations_platform_all ON accounts.account_capacity_reservations FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);

REVOKE ALL ON nodes.nodes, nodes.node_enrollments, nodes.node_sessions,
  endpoints.proxy_endpoints, endpoints.endpoint_status, operations.operations,
  reconciler.runner_desired_states, reconciler.runner_observations,
  reconciler.work_items, reconciler.finalizers
  FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT ON nodes.nodes TO ajiasu_app;
GRANT SELECT, INSERT, UPDATE ON nodes.nodes, nodes.node_enrollments, nodes.node_sessions TO ajiasu_platform;
GRANT SELECT ON accounts.accounts, accounts.account_credentials TO ajiasu_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON accounts.account_capacity_reservations TO ajiasu_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON endpoints.proxy_endpoints, endpoints.endpoint_status,
  operations.operations, reconciler.runner_desired_states, reconciler.runner_observations,
  reconciler.work_items, reconciler.finalizers TO ajiasu_app, ajiasu_platform;

-- +goose Down
REVOKE ALL ON accounts.account_capacity_reservations FROM ajiasu_platform;
REVOKE SELECT ON accounts.accounts, accounts.account_credentials FROM ajiasu_platform;
DROP POLICY phase4_reservations_platform_all ON accounts.account_capacity_reservations;
DROP POLICY phase4_credentials_platform_select ON accounts.account_credentials;
DROP POLICY phase4_accounts_platform_select ON accounts.accounts;
DROP TABLE reconciler.finalizers;
DROP TABLE reconciler.work_items;
DROP TABLE reconciler.runner_observations;
DROP TABLE reconciler.runner_desired_states;
DROP TABLE operations.operations;
DROP TABLE endpoints.endpoint_status;
DROP TABLE endpoints.proxy_endpoints;
DROP TABLE nodes.node_sessions;
DROP TABLE nodes.node_enrollments;
DROP TABLE nodes.nodes;
DROP SCHEMA reconciler;
DROP SCHEMA operations;
DROP SCHEMA endpoints;
DROP SCHEMA nodes;
