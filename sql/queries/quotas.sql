-- name: GetTenantQuota :one
SELECT * FROM tenancy.tenant_quotas WHERE tenant_id = $1;

-- name: LockTenantQuota :one
SELECT * FROM tenancy.tenant_quotas WHERE tenant_id = $1 FOR UPDATE;

