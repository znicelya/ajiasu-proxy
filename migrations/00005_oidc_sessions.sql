-- +goose Up
CREATE TABLE identity.oidc_identities (
    id uuid PRIMARY KEY,
    identity_id uuid NOT NULL UNIQUE REFERENCES identity.user_identities (id) ON DELETE RESTRICT,
    issuer text NOT NULL CHECK (char_length(btrim(issuer)) BETWEEN 1 AND 2048),
    subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 512),
    email text NOT NULL DEFAULT '' CHECK (char_length(email) <= 320),
    display_name text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 200),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (issuer, subject)
);
CREATE INDEX oidc_identities_identity_idx ON identity.oidc_identities (identity_id);

CREATE TABLE identity.oidc_auth_transactions (
    id uuid PRIMARY KEY,
    state_digest bytea NOT NULL UNIQUE CHECK (octet_length(state_digest) = 32),
    binding_digest bytea NOT NULL CHECK (octet_length(binding_digest) = 32),
    nonce_digest bytea NOT NULL CHECK (octet_length(nonce_digest) = 32),
    pkce_verifier_ciphertext bytea NOT NULL CHECK (octet_length(pkce_verifier_ciphertext) >= 29),
    return_path text NOT NULL CHECK (char_length(return_path) BETWEEN 1 AND 2048 AND left(return_path, 1) = '/'),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);
CREATE INDEX oidc_auth_transactions_expiry_idx ON identity.oidc_auth_transactions (expires_at) WHERE consumed_at IS NULL;

CREATE TABLE identity.auth_sessions (
    id uuid PRIMARY KEY,
    identity_id uuid NOT NULL REFERENCES identity.user_identities (id) ON DELETE RESTRICT,
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    csrf_digest bytea NOT NULL CHECK (octet_length(csrf_digest) = 32),
    issued_at timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (last_used_at >= issued_at),
    CHECK (idle_expires_at > issued_at),
    CHECK (absolute_expires_at > issued_at),
    CHECK (idle_expires_at <= absolute_expires_at),
    CHECK (revoked_at IS NULL OR revoked_at >= issued_at)
);
CREATE INDEX auth_sessions_identity_active_idx
    ON identity.auth_sessions (identity_id, absolute_expires_at, id)
    WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_expiry_idx
    ON identity.auth_sessions (idle_expires_at, absolute_expires_at)
    WHERE revoked_at IS NULL;

ALTER TABLE identity.oidc_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.oidc_identities FORCE ROW LEVEL SECURITY;
ALTER TABLE identity.oidc_auth_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.oidc_auth_transactions FORCE ROW LEVEL SECURITY;
ALTER TABLE identity.auth_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.auth_sessions FORCE ROW LEVEL SECURITY;

CREATE POLICY oidc_identities_platform_all ON identity.oidc_identities FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY oidc_auth_transactions_platform_all ON identity.oidc_auth_transactions FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY auth_sessions_platform_all ON identity.auth_sessions FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);

REVOKE ALL ON identity.oidc_identities, identity.oidc_auth_transactions, identity.auth_sessions
    FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT, INSERT, UPDATE ON identity.oidc_identities, identity.oidc_auth_transactions, identity.auth_sessions TO ajiasu_platform;

-- +goose Down
DROP TABLE identity.auth_sessions;
DROP TABLE identity.oidc_auth_transactions;
DROP TABLE identity.oidc_identities;
