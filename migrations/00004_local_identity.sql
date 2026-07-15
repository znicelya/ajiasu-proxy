-- +goose Up
CREATE TABLE identity.user_identities (
    id uuid PRIMARY KEY,
    tenant_eligible boolean NOT NULL DEFAULT true,
    disabled_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (id, tenant_eligible)
);
CREATE INDEX user_identities_created_id_idx ON identity.user_identities (created_at, id);

INSERT INTO identity.user_identities (id, tenant_eligible, disabled_at, version, created_at, updated_at)
SELECT identity_id, true, NULL, 1, min(created_at), max(updated_at)
FROM tenancy.memberships
GROUP BY identity_id
ON CONFLICT (id) DO NOTHING;

ALTER TABLE tenancy.memberships
    ADD COLUMN identity_tenant_eligible boolean NOT NULL DEFAULT true CHECK (identity_tenant_eligible),
    ADD CONSTRAINT memberships_identity_fk
    FOREIGN KEY (identity_id, identity_tenant_eligible)
    REFERENCES identity.user_identities (id, tenant_eligible) ON DELETE RESTRICT;

CREATE TABLE identity.local_admins (
    identity_id uuid PRIMARY KEY,
    tenant_eligible boolean NOT NULL DEFAULT false CHECK (NOT tenant_eligible),
    singleton boolean NOT NULL DEFAULT true UNIQUE CHECK (singleton),
    identifier text NOT NULL UNIQUE
        CHECK (identifier = lower(btrim(identifier)) AND char_length(identifier) BETWEEN 3 AND 254),
    display_name text NOT NULL CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 200),
    password_verifier text NOT NULL CHECK (char_length(password_verifier) BETWEEN 32 AND 1024),
    totp_ciphertext bytea NOT NULL CHECK (octet_length(totp_ciphertext) >= 29),
    failed_attempts integer NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    locked_until timestamptz,
    last_authenticated_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (identity_id, tenant_eligible)
        REFERENCES identity.user_identities (id, tenant_eligible) ON DELETE RESTRICT
);
CREATE INDEX local_admins_locked_until_idx ON identity.local_admins (locked_until) WHERE locked_until IS NOT NULL;

CREATE TABLE identity.local_recovery_codes (
    id uuid PRIMARY KEY,
    identity_id uuid NOT NULL REFERENCES identity.local_admins (identity_id) ON DELETE CASCADE,
    verifier text NOT NULL CHECK (char_length(verifier) BETWEEN 32 AND 1024),
    used_at timestamptz,
    created_at timestamptz NOT NULL,
    UNIQUE (identity_id, verifier)
);
CREATE INDEX local_recovery_codes_identity_unused_idx
    ON identity.local_recovery_codes (identity_id, created_at, id)
    WHERE used_at IS NULL;

CREATE TABLE identity.local_login_attempts (
    id uuid PRIMARY KEY,
    identity_id uuid REFERENCES identity.user_identities (id) ON DELETE RESTRICT,
    identifier_digest bytea NOT NULL CHECK (octet_length(identifier_digest) = 32),
    source_ip inet NOT NULL,
    success boolean NOT NULL,
    reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 1 AND 64),
    attempted_at timestamptz NOT NULL
);
CREATE INDEX local_login_attempts_identity_time_idx
    ON identity.local_login_attempts (identity_id, attempted_at DESC)
    WHERE identity_id IS NOT NULL;
CREATE INDEX local_login_attempts_identifier_time_idx
    ON identity.local_login_attempts (identifier_digest, attempted_at DESC);
CREATE INDEX local_login_attempts_source_time_idx
    ON identity.local_login_attempts (source_ip, attempted_at DESC);

ALTER TABLE identity.user_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.user_identities FORCE ROW LEVEL SECURITY;
ALTER TABLE identity.local_admins ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.local_admins FORCE ROW LEVEL SECURITY;
ALTER TABLE identity.local_recovery_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.local_recovery_codes FORCE ROW LEVEL SECURITY;
ALTER TABLE identity.local_login_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.local_login_attempts FORCE ROW LEVEL SECURITY;

CREATE POLICY user_identities_platform_all ON identity.user_identities FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY local_admins_platform_all ON identity.local_admins FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY local_recovery_codes_platform_all ON identity.local_recovery_codes FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);
CREATE POLICY local_login_attempts_platform_all ON identity.local_login_attempts FOR ALL TO ajiasu_platform USING (true) WITH CHECK (true);

REVOKE ALL ON identity.user_identities, identity.local_admins, identity.local_recovery_codes, identity.local_login_attempts
    FROM PUBLIC, ajiasu_app, ajiasu_platform;
GRANT SELECT, INSERT, UPDATE ON identity.user_identities, identity.local_admins, identity.local_recovery_codes TO ajiasu_platform;
GRANT SELECT, INSERT ON identity.local_login_attempts TO ajiasu_platform;

-- +goose Down
DROP TABLE identity.local_login_attempts;
DROP TABLE identity.local_recovery_codes;
DROP TABLE identity.local_admins;
ALTER TABLE tenancy.memberships DROP CONSTRAINT memberships_identity_fk;
ALTER TABLE tenancy.memberships DROP COLUMN identity_tenant_eligible;
DROP TABLE identity.user_identities;
