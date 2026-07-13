-- name: CreateTenant :one
INSERT INTO tenancy.tenants (id, slug, name, state, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(slug), sqlc.arg(name), 'active', 1, sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING id, slug, name, state, version, created_at, updated_at;

-- name: GetTenantByID :one
SELECT id, slug, name, state, version, created_at, updated_at FROM tenancy.tenants WHERE id = sqlc.arg(id);

-- name: LockTenant :exec
SELECT pg_advisory_xact_lock(hashtextextended('ajiasu-tenant:' || sqlc.arg(tenant_id)::uuid::text, 0));

-- name: UpdateTenant :one
UPDATE tenancy.tenants
SET name = COALESCE(sqlc.narg(name), name), state = COALESCE(sqlc.narg(state), state),
    version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version)
RETURNING id, slug, name, state, version, created_at, updated_at;

-- name: TenantExists :one
SELECT EXISTS (SELECT 1 FROM tenancy.tenants WHERE id = sqlc.arg(id));

-- name: CreateMembership :one
INSERT INTO tenancy.memberships (id, tenant_id, identity_id, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(identity_id), 1, sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING id, tenant_id, identity_id, version, created_at, updated_at;

-- name: GetMembershipByID :one
SELECT id, tenant_id, identity_id, version, created_at, updated_at FROM tenancy.memberships WHERE id = sqlc.arg(id);

-- name: DeleteMembership :execrows
DELETE FROM tenancy.memberships WHERE id = sqlc.arg(id);

-- name: CreateRoleBinding :one
INSERT INTO tenancy.role_bindings (id, tenant_id, membership_id, role, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(membership_id), sqlc.arg(role), 1, sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING id, tenant_id, membership_id, role, version, created_at, updated_at;

-- name: GetRoleBindingByID :one
SELECT id, tenant_id, membership_id, role, version, created_at, updated_at FROM tenancy.role_bindings WHERE id = sqlc.arg(id);

-- name: CountTenantAdminBindings :one
SELECT count(*)
FROM tenancy.role_bindings
WHERE tenant_id = sqlc.arg(tenant_id)
  AND role = 'tenant_admin';

-- name: CountTenantAdminBindingsForMembership :one
SELECT count(*)
FROM tenancy.role_bindings
WHERE tenant_id = sqlc.arg(tenant_id)
  AND membership_id = sqlc.arg(membership_id)
  AND role = 'tenant_admin';

-- name: DeleteRoleBinding :execrows
DELETE FROM tenancy.role_bindings WHERE id = sqlc.arg(id);
