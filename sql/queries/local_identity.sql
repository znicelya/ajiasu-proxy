-- name: CountLocalAdmins :one
SELECT count(*) FROM identity.local_admins;

-- name: LockLocalAdminBootstrap :exec
SELECT pg_advisory_xact_lock(hashtextextended('ajiasu-local-admin-bootstrap', 0));

-- name: LockLocalLoginSource :exec
SELECT pg_advisory_xact_lock(hashtextextended('ajiasu-local-login-source:' || sqlc.arg(source_ip)::inet::text, 0));

-- name: CreateUserIdentity :one
INSERT INTO identity.user_identities (id, tenant_eligible, disabled_at, version, created_at, updated_at)
VALUES (sqlc.arg(id), false, NULL, 1, sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING id, tenant_eligible, disabled_at, version, created_at, updated_at;

-- name: CreateLocalAdmin :one
INSERT INTO identity.local_admins (
    identity_id, identifier, display_name, password_verifier, totp_ciphertext,
    failed_attempts, locked_until, last_authenticated_at, version, created_at, updated_at
)
VALUES (
    sqlc.arg(identity_id), sqlc.arg(identifier), sqlc.arg(display_name),
    sqlc.arg(password_verifier), sqlc.arg(totp_ciphertext), 0, NULL, NULL, 1,
    sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING identity_id, identifier, display_name, password_verifier, totp_ciphertext,
          failed_attempts, locked_until, last_authenticated_at, version, created_at, updated_at;

-- name: CreateLocalRecoveryCode :one
INSERT INTO identity.local_recovery_codes (id, identity_id, verifier, used_at, created_at)
VALUES (sqlc.arg(id), sqlc.arg(identity_id), sqlc.arg(verifier), NULL, sqlc.arg(created_at))
RETURNING id, identity_id, verifier, used_at, created_at;

-- name: GetLocalAdminByIdentifier :one
SELECT admin.identity_id, admin.identifier, admin.display_name, admin.password_verifier,
       admin.totp_ciphertext, admin.failed_attempts, admin.locked_until,
       admin.last_authenticated_at, admin.version, admin.created_at, admin.updated_at,
       principal.disabled_at
FROM identity.local_admins AS admin
JOIN identity.user_identities AS principal ON principal.id = admin.identity_id
WHERE admin.identifier = sqlc.arg(identifier);

-- name: GetLocalAdminByIdentifierForUpdate :one
SELECT admin.identity_id, admin.identifier, admin.display_name, admin.password_verifier,
       admin.totp_ciphertext, admin.failed_attempts, admin.locked_until,
       admin.last_authenticated_at, admin.version, admin.created_at, admin.updated_at,
       principal.disabled_at
FROM identity.local_admins AS admin
JOIN identity.user_identities AS principal ON principal.id = admin.identity_id
WHERE admin.identifier = sqlc.arg(identifier)
FOR UPDATE OF admin;

-- name: ListUnusedLocalRecoveryCodes :many
SELECT id, identity_id, verifier, used_at, created_at
FROM identity.local_recovery_codes
WHERE identity_id = sqlc.arg(identity_id) AND used_at IS NULL
ORDER BY created_at, id;

-- name: ConsumeLocalRecoveryCode :one
UPDATE identity.local_recovery_codes
SET used_at = sqlc.arg(used_at)
WHERE id = sqlc.arg(id) AND identity_id = sqlc.arg(identity_id) AND used_at IS NULL
RETURNING id, identity_id, verifier, used_at, created_at;

-- name: RecordLocalAdminFailure :one
UPDATE identity.local_admins
SET failed_attempts = failed_attempts + 1,
    locked_until = COALESCE(sqlc.narg(locked_until), locked_until),
    version = version + 1,
    updated_at = sqlc.arg(updated_at)
WHERE identity_id = sqlc.arg(identity_id)
RETURNING identity_id, identifier, display_name, password_verifier, totp_ciphertext,
          failed_attempts, locked_until, last_authenticated_at, version, created_at, updated_at;

-- name: ResetLocalAdminFailures :one
UPDATE identity.local_admins
SET failed_attempts = 0,
    locked_until = NULL,
    last_authenticated_at = sqlc.arg(authenticated_at),
    version = version + 1,
    updated_at = sqlc.arg(authenticated_at)
WHERE identity_id = sqlc.arg(identity_id)
RETURNING identity_id, identifier, display_name, password_verifier, totp_ciphertext,
          failed_attempts, locked_until, last_authenticated_at, version, created_at, updated_at;

-- name: RecordLocalLoginAttempt :exec
INSERT INTO identity.local_login_attempts (
    id, identity_id, identifier_digest, source_ip, success, reason, attempted_at
)
VALUES (
    sqlc.arg(id), sqlc.narg(identity_id), sqlc.arg(identifier_digest),
    sqlc.arg(source_ip), sqlc.arg(success), sqlc.arg(reason), sqlc.arg(attempted_at)
);

-- name: CountRecentFailedLocalLoginsBySource :one
SELECT count(*)
FROM identity.local_login_attempts
WHERE source_ip = sqlc.arg(source_ip)
  AND success = false
  AND attempted_at >= sqlc.arg(attempted_since);
