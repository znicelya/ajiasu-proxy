-- name: CreateOIDCAuthTransaction :one
INSERT INTO identity.oidc_auth_transactions (
    id, state_digest, binding_digest, nonce_digest, pkce_verifier_ciphertext, return_path, expires_at, consumed_at, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(state_digest), sqlc.arg(binding_digest), sqlc.arg(nonce_digest), sqlc.arg(pkce_verifier_ciphertext),
    sqlc.arg(return_path), sqlc.arg(expires_at), NULL, sqlc.arg(created_at)
)
RETURNING *;

-- name: ConsumeOIDCAuthTransaction :one
UPDATE identity.oidc_auth_transactions
SET consumed_at = sqlc.arg(consumed_at)
WHERE state_digest = sqlc.arg(state_digest)
  AND binding_digest = sqlc.arg(binding_digest)
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(consumed_at)
RETURNING *;

-- name: GetOIDCIdentity :one
SELECT * FROM identity.oidc_identities WHERE issuer = sqlc.arg(issuer) AND subject = sqlc.arg(subject);

-- name: CreateOIDCUserIdentity :one
INSERT INTO identity.user_identities (id, tenant_eligible, disabled_at, version, created_at, updated_at)
VALUES (sqlc.arg(id), true, NULL, 1, sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING *;

-- name: CreateOIDCIdentity :one
INSERT INTO identity.oidc_identities (id, identity_id, issuer, subject, email, display_name, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(identity_id), sqlc.arg(issuer), sqlc.arg(subject), sqlc.arg(email), sqlc.arg(display_name), sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING *;

-- name: UpdateOIDCIdentityProfile :one
UPDATE identity.oidc_identities
SET email = sqlc.arg(email), display_name = sqlc.arg(display_name), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CreateAuthSession :one
INSERT INTO identity.auth_sessions (
    id, identity_id, token_digest, csrf_digest, issued_at, last_used_at,
    idle_expires_at, absolute_expires_at, revoked_at, version
) VALUES (
    sqlc.arg(id), sqlc.arg(identity_id), sqlc.arg(token_digest), sqlc.arg(csrf_digest),
    sqlc.arg(issued_at), sqlc.arg(issued_at), sqlc.arg(idle_expires_at),
    sqlc.arg(absolute_expires_at), NULL, 1
)
RETURNING *;

-- name: GetAuthSessionByDigestForUpdate :one
SELECT session.*, principal.disabled_at
FROM identity.auth_sessions AS session
JOIN identity.user_identities AS principal ON principal.id = session.identity_id
WHERE session.token_digest = sqlc.arg(token_digest)
FOR UPDATE OF session, principal;

-- name: TouchAuthSession :one
UPDATE identity.auth_sessions
SET last_used_at = sqlc.arg(last_used_at),
    idle_expires_at = LEAST(sqlc.arg(idle_expires_at), absolute_expires_at),
    version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version) AND revoked_at IS NULL
  AND idle_expires_at > sqlc.arg(last_used_at)
  AND absolute_expires_at > sqlc.arg(last_used_at)
RETURNING *;

-- name: RotateAuthSession :one
UPDATE identity.auth_sessions
SET token_digest = sqlc.arg(token_digest), csrf_digest = sqlc.arg(csrf_digest),
    last_used_at = sqlc.arg(rotated_at), idle_expires_at = LEAST(sqlc.arg(idle_expires_at), absolute_expires_at),
    version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version) AND revoked_at IS NULL
  AND idle_expires_at > sqlc.arg(rotated_at)
  AND absolute_expires_at > sqlc.arg(rotated_at)
RETURNING *;

-- name: RevokeAuthSession :execrows
UPDATE identity.auth_sessions
SET revoked_at = sqlc.arg(revoked_at), version = version + 1
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
  AND sqlc.arg(revoked_at)::timestamptz IS NOT NULL;

-- name: LoadSessionTenantGrants :many
SELECT membership.tenant_id, binding.role
FROM tenancy.memberships AS membership
JOIN tenancy.role_bindings AS binding
  ON binding.tenant_id = membership.tenant_id AND binding.membership_id = membership.id
JOIN tenancy.tenants AS tenant ON tenant.id = membership.tenant_id
WHERE membership.identity_id = sqlc.arg(identity_id)
  AND tenant.state = 'active'
ORDER BY membership.tenant_id, binding.role;

-- name: IsLocalAdmin :one
SELECT EXISTS (SELECT 1 FROM identity.local_admins WHERE identity_id = sqlc.arg(identity_id));
