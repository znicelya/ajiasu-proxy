package tenancy_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTenancyMigrationDownAndUpPreservesEarlierFoundation(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	testkit.MigrationsDownTo(t, postgres.AdminDSN, 2)

	admin := openTenancyPool(t, postgres.AdminDSN, false)
	var tenantTable, membershipTable, bindingTable, auditTable, outboxTable, contextFunction bool
	if err := admin.QueryRow(t.Context(), `
SELECT to_regclass('tenancy.tenants') IS NOT NULL,
       to_regclass('tenancy.memberships') IS NOT NULL,
       to_regclass('tenancy.role_bindings') IS NOT NULL,
       to_regclass('audit.audit_events') IS NOT NULL,
       to_regclass('platform.outbox_events') IS NOT NULL,
       to_regprocedure('platform.current_tenant_id()') IS NOT NULL
`).Scan(&tenantTable, &membershipTable, &bindingTable, &auditTable, &outboxTable, &contextFunction); err != nil {
		t.Fatalf("inspect tenancy down-to-2 state: %v", err)
	}
	if tenantTable || membershipTable || bindingTable || !auditTable || !outboxTable || !contextFunction {
		t.Fatalf("down-to-2 state = tenants %t memberships %t bindings %t audit %t outbox %t context %t",
			tenantTable, membershipTable, bindingTable, auditTable, outboxTable, contextFunction)
	}

	testkit.MigrationsUp(t, postgres.AdminDSN)
	if err := admin.QueryRow(t.Context(), `
SELECT to_regclass('tenancy.tenants') IS NOT NULL,
       to_regclass('tenancy.memberships') IS NOT NULL,
       to_regclass('tenancy.role_bindings') IS NOT NULL
`).Scan(&tenantTable, &membershipTable, &bindingTable); err != nil {
		t.Fatalf("inspect tenancy migration reapply: %v", err)
	}
	if !tenantTable || !membershipTable || !bindingTable {
		t.Fatalf("reapplied tenancy tables = tenants %t memberships %t bindings %t", tenantTable, membershipTable, bindingTable)
	}
}

func TestTenancyMigrationSupportsNonSuperuserMigrationOwner(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	admin := openTenancyPool(t, postgres.AdminDSN, false)
	const migratorRole = "ajiasu_test_migrator"
	const migratorPassword = "test-only-migrator-password"
	if _, err := admin.Exec(t.Context(), `
CREATE ROLE ajiasu_test_migrator LOGIN CREATEROLE PASSWORD 'test-only-migrator-password';
GRANT CREATE ON DATABASE ajiasu_test TO ajiasu_test_migrator;
GRANT USAGE, CREATE ON SCHEMA public TO ajiasu_test_migrator;
`); err != nil {
		t.Fatalf("create non-superuser migration owner: %v", err)
	}
	adminURL, err := url.Parse(postgres.AdminDSN)
	if err != nil {
		t.Fatalf("parse admin PostgreSQL DSN: %v", err)
	}
	adminURL.User = url.UserPassword(migratorRole, migratorPassword)
	testkit.MigrationsUp(t, adminURL.String())

	var migratorSuperuser, tenantInsert, membershipInsert, bindingInsert bool
	if err := admin.QueryRow(t.Context(), `
SELECT migrator.rolsuper,
       has_table_privilege('ajiasu_platform', 'tenancy.tenants', 'INSERT'),
       has_table_privilege('ajiasu_platform', 'tenancy.memberships', 'INSERT'),
       has_table_privilege('ajiasu_platform', 'tenancy.role_bindings', 'INSERT')
FROM pg_roles AS migrator
WHERE migrator.rolname = 'ajiasu_test_migrator'
`).Scan(&migratorSuperuser, &tenantInsert, &membershipInsert, &bindingInsert); err != nil {
		t.Fatalf("inspect non-superuser migration result: %v", err)
	}
	if migratorSuperuser || !tenantInsert || !membershipInsert || !bindingInsert {
		t.Fatalf("non-superuser migration result = superuser %t platform inserts tenant %t membership %t binding %t",
			migratorSuperuser, tenantInsert, membershipInsert, bindingInsert)
	}
}

func TestSixTenancyWritesAppendAuditAndOutbox(t *testing.T) {
	db := startTenancyDatabase(t)
	service := tenancy.NewService(db.pools, audit.NewService())
	platformSubject := platformAdminSubject()

	tenant, err := service.CreateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.CreateTenant{
		Slug:                   "six-writes",
		Name:                   "Six Writes",
		InitialAdminIdentityID: createUserIdentity(t, db.admin),
	})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	if tenant.ID == uuid.Nil || tenant.Version != 1 {
		t.Fatalf("created tenant = %#v, want nonzero ID and version 1", tenant)
	}

	updatedName := "Six Atomic Writes"
	tenant, err = service.UpdateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.UpdateTenant{
		TenantID:        tenant.ID,
		ExpectedVersion: tenant.Version,
		Name:            &updatedName,
	})
	if err != nil {
		t.Fatalf("UpdateTenant() error = %v", err)
	}
	if tenant.Version != 2 || tenant.Name != updatedName {
		t.Fatalf("updated tenant = %#v, want version 2 and updated name", tenant)
	}

	tenantSubject := tenantAdminSubject(tenant.ID)
	member, err := service.AddMember(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.AddMember{
		TenantID:   tenant.ID,
		IdentityID: createUserIdentity(t, db.admin),
	})
	if err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}
	binding, err := service.GrantRole(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.GrantRole{
		TenantID:     tenant.ID,
		MembershipID: member.ID,
		Role:         tenancy.Auditor,
	})
	if err != nil {
		t.Fatalf("GrantRole() error = %v", err)
	}
	if err := service.RevokeRole(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), binding.ID); err != nil {
		t.Fatalf("RevokeRole() error = %v", err)
	}
	if err := service.RemoveMember(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), member.ID); err != nil {
		t.Fatalf("RemoveMember() error = %v", err)
	}

	var auditRows, outboxRows, memberRows, bindingRows, lifecycleOutboxRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&auditRows); err != nil {
		t.Fatalf("count tenancy audit rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.outbox_events").Scan(&outboxRows); err != nil {
		t.Fatalf("count tenancy outbox rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), `
SELECT count(*)
FROM platform.outbox_events
WHERE tenant_id = $1
  AND event_type IN ('tenancy.tenant.created', 'tenancy.tenant.updated')
`, tenant.ID).Scan(&lifecycleOutboxRows); err != nil {
		t.Fatalf("count tenant-scoped lifecycle outbox rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.memberships WHERE id = $1", member.ID).Scan(&memberRows); err != nil {
		t.Fatalf("count removed membership: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.role_bindings WHERE id = $1", binding.ID).Scan(&bindingRows); err != nil {
		t.Fatalf("count revoked role binding: %v", err)
	}
	visibleAuditRows, err := database.InTenantTx(t.Context(), db.pools.Tenant, tenant.ID, uuid.New(), func(ctx context.Context, tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(ctx, "SELECT count(*) FROM audit.audit_events").Scan(&count)
		return count, err
	})
	if err != nil {
		t.Fatalf("count tenant-visible audit rows: %v", err)
	}
	otherTenantAuditRows, err := database.InTenantTx(t.Context(), db.pools.Tenant, uuid.New(), uuid.New(), func(ctx context.Context, tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(ctx, "SELECT count(*) FROM audit.audit_events").Scan(&count)
		return count, err
	})
	if err != nil {
		t.Fatalf("count other-tenant-visible audit rows: %v", err)
	}
	if auditRows != 8 || outboxRows != 8 || lifecycleOutboxRows != 2 || visibleAuditRows != 8 || otherTenantAuditRows != 0 || memberRows != 0 || bindingRows != 0 {
		t.Fatalf("six-write result = audit %d outbox %d lifecycle outbox %d tenant-visible audit %d other-visible audit %d member %d binding %d, want 8/8/2/8/0/0/0",
			auditRows, outboxRows, lifecycleOutboxRows, visibleAuditRows, otherTenantAuditRows, memberRows, bindingRows)
	}
}

func TestUpdateTenantRejectsStaleVersionWithoutEvents(t *testing.T) {
	db := startTenancyDatabase(t)
	service := tenancy.NewService(db.pools, audit.NewService())
	subject := platformAdminSubject()
	tenant, err := service.CreateTenant(t.Context(), newPlatformActor(t, subject), tenancy.CreateTenant{Slug: "versioned", Name: "Versioned", InitialAdminIdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	originalVersion := tenant.Version
	firstName := "First Winner"
	updated, err := service.UpdateTenant(t.Context(), newPlatformActor(t, subject), tenancy.UpdateTenant{
		TenantID:        tenant.ID,
		ExpectedVersion: originalVersion,
		Name:            &firstName,
	})
	if err != nil {
		t.Fatalf("first UpdateTenant() error = %v", err)
	}
	noChange, err := service.UpdateTenant(t.Context(), newPlatformActor(t, subject), tenancy.UpdateTenant{
		TenantID:        tenant.ID,
		ExpectedVersion: updated.Version,
		Name:            &firstName,
	})
	if err != nil {
		t.Fatalf("no-op UpdateTenant() error = %v", err)
	}
	if noChange.Version != updated.Version {
		t.Fatalf("no-op UpdateTenant() version = %d, want %d", noChange.Version, updated.Version)
	}
	staleName := "Stale Loser"
	_, err = service.UpdateTenant(t.Context(), newPlatformActor(t, subject), tenancy.UpdateTenant{
		TenantID:        tenant.ID,
		ExpectedVersion: originalVersion,
		Name:            &staleName,
	})
	if !errors.Is(err, tenancy.ErrVersionConflict) {
		t.Fatalf("stale UpdateTenant() error = %v, want ErrVersionConflict", err)
	}
	deleting := tenancy.Deleting
	deleted, err := service.UpdateTenant(t.Context(), newPlatformActor(t, subject), tenancy.UpdateTenant{
		TenantID:        tenant.ID,
		ExpectedVersion: updated.Version,
		State:           &deleting,
	})
	if err != nil {
		t.Fatalf("deleting UpdateTenant() error = %v", err)
	}
	active := tenancy.Active
	_, err = service.UpdateTenant(t.Context(), newPlatformActor(t, subject), tenancy.UpdateTenant{
		TenantID:        tenant.ID,
		ExpectedVersion: originalVersion,
		State:           &active,
	})
	if !errors.Is(err, tenancy.ErrVersionConflict) {
		t.Fatalf("stale invalid-transition UpdateTenant() error = %v, want ErrVersionConflict", err)
	}

	var storedName string
	var storedVersion int64
	var auditRows, outboxRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT name, version FROM tenancy.tenants WHERE id = $1", tenant.ID).Scan(&storedName, &storedVersion); err != nil {
		t.Fatalf("read versioned tenant: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&auditRows); err != nil {
		t.Fatalf("count version audit rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.outbox_events").Scan(&outboxRows); err != nil {
		t.Fatalf("count version outbox rows: %v", err)
	}
	if storedName != firstName || storedVersion != deleted.Version || deleted.Version != originalVersion+2 {
		t.Fatalf("stored tenant after stale write = name %q version %d; successful result %#v", storedName, storedVersion, deleted)
	}
	if auditRows != 5 || outboxRows != 5 {
		t.Fatalf("events after create/two successes/two stale writes = audit %d outbox %d, want 5/5", auditRows, outboxRows)
	}
}

func TestSuspendedTenantRejectsTenantWrites(t *testing.T) {
	db := startTenancyDatabase(t)
	service := tenancy.NewService(db.pools, audit.NewService())
	platformSubject := platformAdminSubject()
	tenant, err := service.CreateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.CreateTenant{Slug: "suspended", Name: "Suspended", InitialAdminIdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	tenantSubject := tenantAdminSubject(tenant.ID)
	member, err := service.AddMember(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.AddMember{TenantID: tenant.ID, IdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("AddMember() setup error = %v", err)
	}
	binding, err := service.GrantRole(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.GrantRole{TenantID: tenant.ID, MembershipID: member.ID, Role: tenancy.Auditor})
	if err != nil {
		t.Fatalf("GrantRole() setup error = %v", err)
	}
	suspended := tenancy.Suspended
	tenant, err = service.UpdateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.UpdateTenant{
		TenantID:        tenant.ID,
		ExpectedVersion: tenant.Version,
		State:           &suspended,
	})
	if err != nil {
		t.Fatalf("suspend tenant: %v", err)
	}

	var auditBefore, outboxBefore int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&auditBefore); err != nil {
		t.Fatalf("count audit before suspended writes: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.outbox_events").Scan(&outboxBefore); err != nil {
		t.Fatalf("count outbox before suspended writes: %v", err)
	}

	writes := []struct {
		name string
		call func() error
	}{
		{name: "add_member", call: func() error {
			_, err := service.AddMember(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.AddMember{TenantID: tenant.ID, IdentityID: uuid.New()})
			return err
		}},
		{name: "remove_member", call: func() error {
			return service.RemoveMember(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), member.ID)
		}},
		{name: "grant_role", call: func() error {
			_, err := service.GrantRole(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.GrantRole{TenantID: tenant.ID, MembershipID: member.ID, Role: tenancy.Operator})
			return err
		}},
		{name: "revoke_role", call: func() error {
			return service.RevokeRole(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), binding.ID)
		}},
	}
	for _, write := range writes {
		t.Run(write.name, func(t *testing.T) {
			if err := write.call(); !errors.Is(err, tenancy.ErrTenantSuspended) {
				t.Fatalf("suspended write error = %v, want ErrTenantSuspended", err)
			}
		})
	}

	var memberRows, bindingRows, auditAfter, outboxAfter int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.memberships WHERE id = $1", member.ID).Scan(&memberRows); err != nil {
		t.Fatalf("count retained membership: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.role_bindings WHERE id = $1", binding.ID).Scan(&bindingRows); err != nil {
		t.Fatalf("count retained binding: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&auditAfter); err != nil {
		t.Fatalf("count audit after suspended writes: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.outbox_events").Scan(&outboxAfter); err != nil {
		t.Fatalf("count outbox after suspended writes: %v", err)
	}
	if memberRows != 1 || bindingRows != 1 || auditAfter != auditBefore || outboxAfter != outboxBefore {
		t.Fatalf("suspended side effects = member %d binding %d audit %d/%d outbox %d/%d", memberRows, bindingRows, auditAfter, auditBefore, outboxAfter, outboxBefore)
	}
}

func TestRemoveMemberCascadesRoleBindings(t *testing.T) {
	db := startTenancyDatabase(t)
	service := tenancy.NewService(db.pools, audit.NewService())
	platformSubject := platformAdminSubject()
	tenant, err := service.CreateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.CreateTenant{Slug: "cascade", Name: "Cascade", InitialAdminIdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	tenantSubject := tenantAdminSubject(tenant.ID)
	member, err := service.AddMember(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.AddMember{TenantID: tenant.ID, IdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}
	for _, role := range []tenancy.Role{tenancy.Operator, tenancy.Auditor} {
		if _, err := service.GrantRole(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), tenancy.GrantRole{TenantID: tenant.ID, MembershipID: member.ID, Role: role}); err != nil {
			t.Fatalf("GrantRole(%s) error = %v", role, err)
		}
	}
	if err := service.RemoveMember(t.Context(), newTenantActor(t, tenantSubject, tenant.ID), member.ID); err != nil {
		t.Fatalf("RemoveMember() error = %v", err)
	}

	var members, bindings int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.memberships WHERE id = $1", member.ID).Scan(&members); err != nil {
		t.Fatalf("count cascaded membership: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.role_bindings WHERE membership_id = $1", member.ID).Scan(&bindings); err != nil {
		t.Fatalf("count cascaded bindings: %v", err)
	}
	if members != 0 || bindings != 0 {
		t.Fatalf("cascade result = memberships %d bindings %d, want 0/0", members, bindings)
	}
}

func TestCannotRemoveOrRevokeLastTenantAdmin(t *testing.T) {
	db := startTenancyDatabase(t)
	service := tenancy.NewService(db.pools, audit.NewService())
	initialAdminIdentityID := createUserIdentity(t, db.admin)
	tenant, err := service.CreateTenant(t.Context(), newPlatformActor(t, platformAdminSubject()), tenancy.CreateTenant{Slug: "last-admin", Name: "Last Admin", InitialAdminIdentityID: initialAdminIdentityID})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	actor := newTenantActor(t, tenantAdminSubject(tenant.ID), tenant.ID)
	var member tenancy.Membership
	if err := db.admin.QueryRow(t.Context(), `
SELECT id, tenant_id, identity_id, version, created_at, updated_at
FROM tenancy.memberships
WHERE tenant_id = $1 AND identity_id = $2
`, tenant.ID, initialAdminIdentityID).Scan(&member.ID, &member.TenantID, &member.UserID, &member.Version, &member.CreatedAt, &member.UpdatedAt); err != nil {
		t.Fatalf("load initial-admin membership: %v", err)
	}
	var binding tenancy.RoleBinding
	if err := db.admin.QueryRow(t.Context(), `
SELECT id, tenant_id, membership_id, role, version, created_at, updated_at
FROM tenancy.role_bindings
WHERE tenant_id = $1 AND membership_id = $2 AND role = 'tenant_admin'
`, tenant.ID, member.ID).Scan(&binding.ID, &binding.TenantID, &binding.MembershipID, &binding.Role, &binding.Version, &binding.CreatedAt, &binding.UpdatedAt); err != nil {
		t.Fatalf("load initial-admin binding: %v", err)
	}
	if err := service.RevokeRole(t.Context(), actor, binding.ID); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("RevokeRole(last admin) error = %v, want ErrForbidden", err)
	}
	if err := service.RemoveMember(t.Context(), actor, member.ID); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("RemoveMember(last admin) error = %v, want ErrForbidden", err)
	}
	var members, bindings int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.memberships WHERE id = $1", member.ID).Scan(&members); err != nil {
		t.Fatalf("count last-admin membership: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.role_bindings WHERE id = $1", binding.ID).Scan(&bindings); err != nil {
		t.Fatalf("count last-admin binding: %v", err)
	}
	if members != 1 || bindings != 1 {
		t.Fatalf("last-admin rows after rejected mutations = membership %d binding %d, want 1/1", members, bindings)
	}
}

func TestTenantLifecycleAndWritesShareSerializationLock(t *testing.T) {
	db := startTenancyDatabase(t)
	service := tenancy.NewService(db.pools, audit.NewService())
	platformSubject := platformAdminSubject()
	tenant, err := service.CreateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.CreateTenant{Slug: "serialized", Name: "Serialized", InitialAdminIdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}

	release := holdTenantAdvisoryLock(t, db.admin, tenant.ID)
	suspended := tenancy.Suspended
	deadlineContext, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	_, err = service.UpdateTenant(deadlineContext, newPlatformActor(t, platformSubject), tenancy.UpdateTenant{
		TenantID: tenant.ID, ExpectedVersion: tenant.Version, State: &suspended,
	})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		release()
		t.Fatalf("UpdateTenant() while tenant lock held error = %v, want context deadline", err)
	}
	release()

	release = holdTenantAdvisoryLock(t, db.admin, tenant.ID)
	deadlineContext, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
	_, err = service.AddMember(deadlineContext, newTenantActor(t, tenantAdminSubject(tenant.ID), tenant.ID), tenancy.AddMember{
		TenantID: tenant.ID, IdentityID: uuid.New(),
	})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		release()
		t.Fatalf("AddMember() while tenant lock held error = %v, want context deadline", err)
	}
	release()
}

func TestTenancyRLSIsolatesTenantsAndConnectionReuse(t *testing.T) {
	db := startTenancyDatabase(t)
	var initialBackendPID int
	if err := db.pools.Tenant.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&initialBackendPID); err != nil {
		t.Fatalf("read initial tenant-pool backend PID: %v", err)
	}
	service := tenancy.NewService(db.pools, audit.NewService())
	platformSubject := platformAdminSubject()
	tenantA, err := service.CreateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.CreateTenant{Slug: "tenant-a", Name: "Tenant A", InitialAdminIdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("create Tenant A: %v", err)
	}
	tenantB, err := service.CreateTenant(t.Context(), newPlatformActor(t, platformSubject), tenancy.CreateTenant{Slug: "tenant-b", Name: "Tenant B", InitialAdminIdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("create Tenant B: %v", err)
	}
	actorA := tenantAdminSubject(tenantA.ID)
	actorB := tenantAdminSubject(tenantB.ID)
	memberA, err := service.AddMember(t.Context(), newTenantActor(t, actorA, tenantA.ID), tenancy.AddMember{TenantID: tenantA.ID, IdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("add Tenant A member: %v", err)
	}
	memberB, err := service.AddMember(t.Context(), newTenantActor(t, actorB, tenantB.ID), tenancy.AddMember{TenantID: tenantB.ID, IdentityID: createUserIdentity(t, db.admin)})
	if err != nil {
		t.Fatalf("add Tenant B member: %v", err)
	}
	bindingA, err := service.GrantRole(t.Context(), newTenantActor(t, actorA, tenantA.ID), tenancy.GrantRole{TenantID: tenantA.ID, MembershipID: memberA.ID, Role: tenancy.Auditor})
	if err != nil {
		t.Fatalf("grant Tenant A role: %v", err)
	}
	bindingB, err := service.GrantRole(t.Context(), newTenantActor(t, actorB, tenantB.ID), tenancy.GrantRole{TenantID: tenantB.ID, MembershipID: memberB.ID, Role: tenancy.Auditor})
	if err != nil {
		t.Fatalf("grant Tenant B role: %v", err)
	}

	assertTenantRows(t, db.pools.Tenant, tenantA.ID, actorA.ActorID, memberA.ID, bindingA.ID)
	assertTenantRows(t, db.pools.Tenant, tenantB.ID, actorB.ActorID, memberB.ID, bindingB.ID)
	assertNoCrossTenantMutations(t, db.pools.Tenant, tenantA.ID, actorA.ActorID, tenantB.ID, memberB.ID, bindingB.ID)

	var membersWithoutContext, bindingsWithoutContext int
	if err := db.pools.Tenant.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.memberships").Scan(&membersWithoutContext); err != nil {
		t.Fatalf("query memberships without context: %v", err)
	}
	if err := db.pools.Tenant.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.role_bindings").Scan(&bindingsWithoutContext); err != nil {
		t.Fatalf("query role bindings without context: %v", err)
	}
	if membersWithoutContext != 0 || bindingsWithoutContext != 0 {
		t.Fatalf("rows without tenant context = memberships %d bindings %d, want 0/0", membersWithoutContext, bindingsWithoutContext)
	}
	var finalBackendPID int
	if err := db.pools.Tenant.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&finalBackendPID); err != nil {
		t.Fatalf("read final tenant-pool backend PID: %v", err)
	}
	if finalBackendPID != initialBackendPID {
		t.Fatalf("tenant pool backend PID changed from %d to %d; connection reuse was not proven", initialBackendPID, finalBackendPID)
	}
}

type tenancyDatabase struct {
	admin *pgxpool.Pool
	pools *database.Pools
}

func startTenancyDatabase(t *testing.T) tenancyDatabase {
	t.Helper()
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	tenantPool := openTenancyPool(t, postgres.TenantDSN, true)
	platformPool := openTenancyPool(t, postgres.PlatformDSN, true)
	return tenancyDatabase{
		admin: openTenancyPool(t, postgres.AdminDSN, false),
		pools: &database.Pools{Tenant: tenantPool, Platform: platformPool},
	}
}

func openTenancyPool(t *testing.T, dsn string, singleConnection bool) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse tenancy PostgreSQL pool config: %v", err)
	}
	if singleConnection {
		config.MaxConns = 1
		config.MinConns = 1
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open tenancy PostgreSQL pool: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Fatalf("ping tenancy PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func holdTenantAdvisoryLock(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) func() {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin tenant advisory-lock transaction: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
SELECT pg_advisory_xact_lock(hashtextextended('ajiasu-tenant:' || $1::uuid::text, 0))
`, tenantID); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("acquire tenant advisory lock: %v", err)
	}
	return func() {
		if err := tx.Rollback(t.Context()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Fatalf("release tenant advisory lock: %v", err)
		}
	}
}

func createUserIdentity(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	if _, err := pool.Exec(t.Context(), `
INSERT INTO identity.user_identities (id, disabled_at, version, created_at, updated_at)
VALUES ($1, NULL, 1, $2, $2)
`, id, now); err != nil {
		t.Fatalf("create tenancy-test user identity: %v", err)
	}
	return id
}

func platformAdminSubject() tenancy.Subject {
	return tenancy.Subject{ActorID: uuid.New(), PlatformRoles: []tenancy.Role{tenancy.PlatformAdmin}}
}

func tenantAdminSubject(tenantID uuid.UUID) tenancy.Subject {
	return tenancy.Subject{
		ActorID: uuid.New(),
		TenantGrants: []tenancy.TenantGrant{
			{TenantID: tenantID, Role: tenancy.TenantAdmin},
		},
	}
}

func newPlatformActor(t *testing.T, subject tenancy.Subject) tenancy.PlatformActor {
	t.Helper()
	actor, err := tenancy.NewPlatformActor(subject, validActorMetadata())
	if err != nil {
		t.Fatalf("construct platform actor: %v", err)
	}
	return actor
}

func newTenantActor(t *testing.T, subject tenancy.Subject, tenantID uuid.UUID) tenancy.TenantActor {
	t.Helper()
	actor, err := tenancy.NewTenantActor(subject, tenantID, validActorMetadata())
	if err != nil {
		t.Fatalf("construct tenant actor: %v", err)
	}
	return actor
}

func assertTenantRows(t *testing.T, pool *pgxpool.Pool, tenantID, actorID, expectedMemberID, expectedBindingID uuid.UUID) {
	t.Helper()
	counts, err := database.InTenantTx(t.Context(), pool, tenantID, actorID, func(ctx context.Context, tx pgx.Tx) ([3]int, error) {
		var result [3]int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM tenancy.tenants").Scan(&result[0]); err != nil {
			return result, err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM tenancy.memberships WHERE id = $1", expectedMemberID).Scan(&result[1]); err != nil {
			return result, err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM tenancy.role_bindings WHERE id = $1", expectedBindingID).Scan(&result[2]); err != nil {
			return result, err
		}
		return result, nil
	})
	if err != nil {
		t.Fatalf("query tenant-scoped rows: %v", err)
	}
	if counts != [3]int{1, 1, 1} {
		t.Fatalf("tenant %s visible rows = tenants %d memberships %d bindings %d, want 1/1/1", tenantID, counts[0], counts[1], counts[2])
	}
}

func assertNoCrossTenantMutations(t *testing.T, pool *pgxpool.Pool, contextTenantID, actorID, otherTenantID, otherMemberID, otherBindingID uuid.UUID) {
	t.Helper()
	_, err := database.InTenantTx(t.Context(), pool, contextTenantID, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		var visibleMembers, visibleBindings int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM tenancy.memberships WHERE id = $1", otherMemberID).Scan(&visibleMembers); err != nil {
			return struct{}{}, err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM tenancy.role_bindings WHERE id = $1", otherBindingID).Scan(&visibleBindings); err != nil {
			return struct{}{}, err
		}
		if visibleMembers != 0 || visibleBindings != 0 {
			return struct{}{}, fmt.Errorf("cross-tenant rows visible: memberships %d bindings %d", visibleMembers, visibleBindings)
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("cross-tenant visibility checks: %v", err)
	}

	mutations := []struct {
		name string
		sql  string
		id   uuid.UUID
	}{
		{name: "update_membership", sql: "UPDATE tenancy.memberships SET version = version + 1 WHERE id = $1", id: otherMemberID},
		{name: "delete_membership", sql: "DELETE FROM tenancy.memberships WHERE id = $1", id: otherMemberID},
		{name: "update_binding", sql: "UPDATE tenancy.role_bindings SET version = version + 1 WHERE id = $1", id: otherBindingID},
		{name: "delete_binding", sql: "DELETE FROM tenancy.role_bindings WHERE id = $1", id: otherBindingID},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			tag, err := database.InTenantTx(t.Context(), pool, contextTenantID, actorID, func(ctx context.Context, tx pgx.Tx) (pgconn.CommandTag, error) {
				return tx.Exec(ctx, mutation.sql, mutation.id)
			})
			if err != nil {
				if !isInsufficientPrivilege(err) {
					t.Fatalf("cross-tenant mutation error = %v, want RLS/privilege denial", err)
				}
				return
			}
			if tag.RowsAffected() != 0 {
				t.Fatalf("cross-tenant mutation affected %d rows", tag.RowsAffected())
			}
		})
	}

	now := time.Now().UTC()
	insertions := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "insert_membership",
			sql:  "INSERT INTO tenancy.memberships (id, tenant_id, identity_id, version, created_at, updated_at) VALUES ($1, $2, $3, 1, $4, $4)",
			args: []any{uuid.New(), otherTenantID, uuid.New(), now},
		},
		{
			name: "insert_binding",
			sql:  "INSERT INTO tenancy.role_bindings (id, tenant_id, membership_id, role, version, created_at, updated_at) VALUES ($1, $2, $3, 'auditor', 1, $4, $4)",
			args: []any{uuid.New(), otherTenantID, otherMemberID, now},
		},
	}
	for _, insertion := range insertions {
		t.Run(insertion.name, func(t *testing.T) {
			_, err := database.InTenantTx(t.Context(), pool, contextTenantID, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
				_, err := tx.Exec(ctx, insertion.sql, insertion.args...)
				return struct{}{}, err
			})
			if err == nil {
				t.Fatal("cross-tenant insertion succeeded")
			}
			if !isInsufficientPrivilege(err) {
				t.Fatalf("cross-tenant insertion error = %v, want RLS/privilege denial", err)
			}
		})
	}
}

func isInsufficientPrivilege(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}
