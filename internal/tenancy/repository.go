package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/tenancy/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func createTenant(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID, slug, name string, now time.Time) (dbgen.TenancyTenant, error) {
	return dbgen.New(executor).CreateTenant(ctx, dbgen.CreateTenantParams{ID: tenantID, Slug: slug, Name: name, CreatedAt: now, UpdatedAt: now})
}

func createDefaultQuota(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID, now time.Time) error {
	_, err := executor.Exec(ctx, `INSERT INTO tenancy.tenant_quotas (tenant_id, created_at, updated_at) VALUES ($1, $2, $2)`, tenantID, now)
	return err
}

func userIdentityExists(ctx context.Context, executor dbgen.DBTX, identityID uuid.UUID) (bool, error) {
	return dbgen.New(executor).UserIdentityExists(ctx, identityID)
}

func getTenant(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID) (dbgen.TenancyTenant, error) {
	return dbgen.New(executor).GetTenantByID(ctx, tenantID)
}

func listTenants(ctx context.Context, executor dbgen.DBTX, after time.Time, afterID uuid.UUID, pageSize int32) ([]dbgen.TenancyTenant, error) {
	return dbgen.New(executor).ListTenants(ctx, dbgen.ListTenantsParams{AfterCreatedAt: after, AfterID: afterID, PageSize: pageSize})
}

func lockTenant(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID) error {
	return dbgen.New(executor).LockTenant(ctx, tenantID)
}

func updateTenant(ctx context.Context, executor dbgen.DBTX, command UpdateTenant, now time.Time) (dbgen.TenancyTenant, error) {
	name := pgtype.Text{}
	if command.Name != nil {
		name = pgtype.Text{String: *command.Name, Valid: true}
	}
	state := pgtype.Text{}
	if command.State != nil {
		state = pgtype.Text{String: string(*command.State), Valid: true}
	}
	return dbgen.New(executor).UpdateTenant(ctx, dbgen.UpdateTenantParams{
		Name: name, State: state, UpdatedAt: now, ID: command.TenantID, ExpectedVersion: command.ExpectedVersion,
	})
}

func tenantExists(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID) (bool, error) {
	return dbgen.New(executor).TenantExists(ctx, tenantID)
}

func createMembership(ctx context.Context, executor dbgen.DBTX, membershipID, tenantID, userID uuid.UUID, now time.Time) (dbgen.TenancyMembership, error) {
	return dbgen.New(executor).CreateMembership(ctx, dbgen.CreateMembershipParams{ID: membershipID, TenantID: tenantID, IdentityID: userID, CreatedAt: now, UpdatedAt: now})
}

func getMembership(ctx context.Context, executor dbgen.DBTX, membershipID uuid.UUID) (dbgen.TenancyMembership, error) {
	return dbgen.New(executor).GetMembershipByID(ctx, membershipID)
}

func listMemberships(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID, after time.Time, afterID uuid.UUID, pageSize int32) ([]dbgen.TenancyMembership, error) {
	return dbgen.New(executor).ListMemberships(ctx, dbgen.ListMembershipsParams{TenantID: tenantID, AfterCreatedAt: after, AfterID: afterID, PageSize: pageSize})
}

func deleteMembership(ctx context.Context, executor dbgen.DBTX, membershipID uuid.UUID) (int64, error) {
	return dbgen.New(executor).DeleteMembership(ctx, membershipID)
}

func createRoleBinding(ctx context.Context, executor dbgen.DBTX, bindingID, tenantID, membershipID uuid.UUID, role Role, now time.Time) (dbgen.TenancyRoleBinding, error) {
	return dbgen.New(executor).CreateRoleBinding(ctx, dbgen.CreateRoleBindingParams{ID: bindingID, TenantID: tenantID, MembershipID: membershipID, Role: string(role), CreatedAt: now, UpdatedAt: now})
}

func getRoleBinding(ctx context.Context, executor dbgen.DBTX, bindingID uuid.UUID) (dbgen.TenancyRoleBinding, error) {
	return dbgen.New(executor).GetRoleBindingByID(ctx, bindingID)
}

func listRoleBindings(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID, after time.Time, afterID uuid.UUID, pageSize int32) ([]dbgen.TenancyRoleBinding, error) {
	return dbgen.New(executor).ListRoleBindings(ctx, dbgen.ListRoleBindingsParams{TenantID: tenantID, AfterCreatedAt: after, AfterID: afterID, PageSize: pageSize})
}

func countTenantAdminBindings(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID) (int64, error) {
	return dbgen.New(executor).CountTenantAdminBindings(ctx, tenantID)
}

func countTenantAdminBindingsForMembership(ctx context.Context, executor dbgen.DBTX, tenantID, membershipID uuid.UUID) (int64, error) {
	return dbgen.New(executor).CountTenantAdminBindingsForMembership(ctx, dbgen.CountTenantAdminBindingsForMembershipParams{
		TenantID: tenantID, MembershipID: membershipID,
	})
}

func deleteRoleBinding(ctx context.Context, executor dbgen.DBTX, bindingID uuid.UUID) (int64, error) {
	return dbgen.New(executor).DeleteRoleBinding(ctx, bindingID)
}

func mapStorageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		switch pgErr.SQLState() {
		case "23505":
			return ErrAlreadyExists
		case "23503", "23514", "22P02":
			return ErrInvalidArgument
		}
	}
	return fmt.Errorf("%w: %w", ErrStorage, err)
}
