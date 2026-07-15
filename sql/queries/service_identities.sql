-- name: CreateServiceIdentity :one
INSERT INTO identity.service_identities (id, scope, tenant_id, name, disabled_at, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(scope), sqlc.narg(tenant_id), sqlc.arg(name), NULL, 1, sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING *;

-- name: GetServiceIdentity :one
SELECT * FROM identity.service_identities WHERE id = sqlc.arg(id);

-- name: GetServiceIdentityForUpdate :one
SELECT * FROM identity.service_identities WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: ListServiceIdentities :many
SELECT * FROM identity.service_identities
WHERE (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope))
  AND (sqlc.narg(tenant_id)::uuid IS NULL OR tenant_id = sqlc.narg(tenant_id))
  AND (created_at, id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY created_at, id
LIMIT sqlc.arg(page_size);

-- name: DisableServiceIdentity :one
UPDATE identity.service_identities
SET disabled_at = sqlc.arg(disabled_at)::timestamptz, updated_at = sqlc.arg(updated_at), version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version) AND disabled_at IS NULL
RETURNING *;

-- name: CreateServiceToken :one
INSERT INTO identity.service_tokens (
    id, service_identity_id, scope, tenant_id, prefix, verifier, role, source_cidr,
    expires_at, revoked_at, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(service_identity_id), sqlc.arg(scope), sqlc.narg(tenant_id),
    sqlc.arg(prefix), sqlc.arg(verifier), sqlc.arg(role), sqlc.narg(source_cidr),
    sqlc.arg(expires_at), NULL, sqlc.arg(created_at)
)
RETURNING *;

-- name: CountActiveServiceTokens :one
SELECT count(*) FROM identity.service_tokens
WHERE service_identity_id = sqlc.arg(service_identity_id)
  AND revoked_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: FindServiceTokenCandidates :many
SELECT token.*, principal.name AS service_identity_name, principal.disabled_at AS service_identity_disabled_at
FROM identity.service_tokens AS token
JOIN identity.service_identities AS principal ON principal.id = token.service_identity_id
WHERE token.prefix = sqlc.arg(prefix)
  AND token.scope = sqlc.arg(scope)
  AND token.tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
ORDER BY token.created_at DESC, token.id
LIMIT 8;

-- name: GetServiceTokenForAuthentication :one
SELECT token.*
FROM identity.service_tokens AS token
WHERE token.id = sqlc.arg(id)
  AND token.service_identity_id = sqlc.arg(service_identity_id)
  AND token.scope = sqlc.arg(scope)
  AND token.tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
FOR SHARE OF token;

-- name: GetServiceIdentityForAuthentication :one
SELECT * FROM identity.service_identities
WHERE id = sqlc.arg(id)
FOR SHARE;

-- name: ListServiceTokens :many
SELECT id, service_identity_id, scope, tenant_id, prefix, role, source_cidr, expires_at, revoked_at, created_at
FROM identity.service_tokens
WHERE service_identity_id = sqlc.arg(service_identity_id)
ORDER BY created_at, id;

-- name: RevokeServiceToken :one
UPDATE identity.service_tokens
SET revoked_at = sqlc.arg(revoked_at)::timestamptz
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING *;

-- name: RevokeActiveServiceIdentityTokens :execrows
UPDATE identity.service_tokens
SET revoked_at = sqlc.arg(revoked_at)::timestamptz
WHERE service_identity_id = sqlc.arg(service_identity_id)
  AND revoked_at IS NULL;
