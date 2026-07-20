package tenancy

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	pools *database.Pools
	audit audit.Service
	now   func() time.Time
	newID func() (uuid.UUID, error)
}

func NewService(pools *database.Pools, auditService audit.Service) *Service {
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &Service{
		pools: pools,
		audit: auditService,
		now:   func() time.Time { return time.Now().UTC() },
		newID: uuid.NewV7,
	}
}

func (s *Service) GetTenant(ctx context.Context, actor PlatformActor, tenantID uuid.UUID) (Tenant, error) {
	if tenantID == uuid.Nil || !Authorize(actor.subject, ActionUpdateTenant, Target{Scope: ScopePlatform}).Allowed {
		return Tenant{}, ErrForbidden
	}
	row, err := database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (dbgen.TenancyTenant, error) {
		return getTenant(ctx, tx, tenantID)
	})
	if err != nil {
		return Tenant{}, mapStorageError(err)
	}
	return mapTenant(row), nil
}

func (s *Service) ListTenants(ctx context.Context, actor PlatformActor, after time.Time, afterID uuid.UUID, pageSize int32) ([]Tenant, error) {
	if pageSize < 1 || pageSize > 200 || !Authorize(actor.subject, ActionUpdateTenant, Target{Scope: ScopePlatform}).Allowed {
		return nil, ErrForbidden
	}
	rows, err := database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]dbgen.TenancyTenant, error) {
		return listTenants(ctx, tx, after, afterID, pageSize)
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	result := make([]Tenant, len(rows))
	for index := range rows {
		result[index] = mapTenant(rows[index])
	}
	return result, nil
}

func (s *Service) ListMembers(ctx context.Context, actor TenantActor, after time.Time, afterID uuid.UUID, pageSize int32) ([]Membership, error) {
	if pageSize < 1 || pageSize > 200 || !Authorize(actor.subject, ActionAddMember, tenantTarget(actor.tenantID)).Allowed {
		return nil, ErrForbidden
	}
	rows, err := database.InTenantTx(ctx, s.pools.Tenant, actor.tenantID, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]dbgen.TenancyMembership, error) {
		return listMemberships(ctx, tx, actor.tenantID, after, afterID, pageSize)
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	result := make([]Membership, len(rows))
	for index := range rows {
		result[index] = mapMembership(rows[index])
	}
	return result, nil
}

func (s *Service) ListRoleBindings(ctx context.Context, actor TenantActor, after time.Time, afterID uuid.UUID, pageSize int32) ([]RoleBinding, error) {
	if pageSize < 1 || pageSize > 200 || !Authorize(actor.subject, ActionGrantRole, tenantTarget(actor.tenantID)).Allowed {
		return nil, ErrForbidden
	}
	rows, err := database.InTenantTx(ctx, s.pools.Tenant, actor.tenantID, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]dbgen.TenancyRoleBinding, error) {
		return listRoleBindings(ctx, tx, actor.tenantID, after, afterID, pageSize)
	})
	if err != nil {
		return nil, mapStorageError(err)
	}
	result := make([]RoleBinding, len(rows))
	for index := range rows {
		result[index] = mapRoleBinding(rows[index])
	}
	return result, nil
}

func (s *Service) CreateTenant(ctx context.Context, actor PlatformActor, command CreateTenant) (Tenant, error) {
	if err := command.Validate(); err != nil {
		return Tenant{}, err
	}
	if !Authorize(actor.subject, ActionCreateTenant, Target{Scope: ScopePlatform}).Allowed {
		return Tenant{}, ErrForbidden
	}
	slug := normalizeSlug(command)
	if !validSlug(slug) {
		return Tenant{}, ErrInvalidArgument
	}
	tenantID, err := s.newID()
	if err != nil {
		return Tenant{}, ErrStorage
	}
	membershipID, err := s.newID()
	if err != nil {
		return Tenant{}, ErrStorage
	}
	bindingID, err := s.newID()
	if err != nil {
		return Tenant{}, ErrStorage
	}
	now := s.now().UTC()
	var result Tenant
	_, err = database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		exists, err := userIdentityExists(ctx, tx, command.InitialAdminIdentityID)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if !exists {
			return struct{}{}, ErrInvalidArgument
		}
		row, err := createTenant(ctx, tx, tenantID, slug, strings.TrimSpace(command.Name), now)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if err := createDefaultQuota(ctx, tx, tenantID, now); err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if _, err := createMembership(ctx, tx, membershipID, tenantID, command.InitialAdminIdentityID, now); err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if _, err := createRoleBinding(ctx, tx, bindingID, tenantID, membershipID, TenantAdmin, now); err != nil {
			return struct{}{}, mapStorageError(err)
		}
		result = mapTenant(row)
		if err := s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &result.ID, "tenancy.tenant.created", "tenant", result.ID, "success", map[string]any{
			"tenant_id": result.ID.String(), "version": result.Version,
		}, audit.OutboxEvent{EventType: "tenancy.tenant.created", AggregateType: "tenant", AggregateID: result.ID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": result.ID.String(), "version": result.Version,
		}, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		if err := s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &result.ID, "tenancy.membership.added", "membership", membershipID, "success", map[string]any{
			"tenant_id": result.ID.String(), "membership_id": membershipID.String(), "identity_id": command.InitialAdminIdentityID.String(), "initial_admin": true,
		}, audit.OutboxEvent{EventType: "tenancy.membership.added", AggregateType: "membership", AggregateID: membershipID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": result.ID.String(), "membership_id": membershipID.String(), "initial_admin": true,
		}, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		if err := s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &result.ID, "tenancy.role.granted", "role_binding", bindingID, "success", map[string]any{
			"tenant_id": result.ID.String(), "membership_id": membershipID.String(), "role": string(TenantAdmin), "initial_admin": true,
		}, audit.OutboxEvent{EventType: "tenancy.role.granted", AggregateType: "role_binding", AggregateID: bindingID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": result.ID.String(), "membership_id": membershipID.String(), "role": string(TenantAdmin), "initial_admin": true,
		}, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil {
		return Tenant{}, err
	}
	return result, nil
}

func (s *Service) UpdateTenant(ctx context.Context, actor PlatformActor, command UpdateTenant) (Tenant, error) {
	if err := command.Validate(); err != nil {
		return Tenant{}, err
	}
	if !Authorize(actor.subject, ActionUpdateTenant, Target{Scope: ScopePlatform}).Allowed {
		return Tenant{}, ErrForbidden
	}
	now := s.now().UTC()
	var result Tenant
	_, err := database.InPlatformTx(ctx, s.pools.Platform, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if err := lockTenant(ctx, tx, command.TenantID); err != nil {
			return struct{}{}, mapStorageError(err)
		}
		current, err := getTenant(ctx, tx, command.TenantID)
		if err != nil {
			mapped := mapStorageError(err)
			if errors.Is(mapped, ErrNotFound) {
				return struct{}{}, ErrNotFound
			}
			return struct{}{}, mapped
		}
		if current.Version != command.ExpectedVersion {
			return struct{}{}, ErrVersionConflict
		}
		changed := false
		if command.Name != nil {
			normalizedName := strings.TrimSpace(*command.Name)
			command.Name = &normalizedName
			changed = normalizedName != current.Name
		}
		if command.State != nil && *command.State != TenantState(current.State) {
			if err := ValidateTenantStateTransition(TenantState(current.State), *command.State); err != nil {
				return struct{}{}, err
			}
			changed = true
		}
		if !changed {
			result = mapTenant(current)
			return struct{}{}, nil
		}
		updated, err := updateTenant(ctx, tx, command, now)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				exists, existsErr := tenantExists(ctx, tx, command.TenantID)
				if existsErr != nil {
					return struct{}{}, mapStorageError(existsErr)
				}
				if exists {
					return struct{}{}, ErrVersionConflict
				}
				return struct{}{}, ErrNotFound
			}
			return struct{}{}, mapStorageError(err)
		}
		result = mapTenant(updated)
		if err := s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &result.ID, "tenancy.tenant.updated", "tenant", result.ID, "success", map[string]any{
			"tenant_id": result.ID.String(), "expected_version": command.ExpectedVersion, "version": result.Version,
		}, audit.OutboxEvent{EventType: "tenancy.tenant.updated", AggregateType: "tenant", AggregateID: result.ID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": result.ID.String(), "version": result.Version,
		}, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil {
		return Tenant{}, err
	}
	return result, nil
}

func (s *Service) AddMember(ctx context.Context, actor TenantActor, command AddMember) (Membership, error) {
	if err := command.Validate(); err != nil {
		return Membership{}, err
	}
	if command.TenantID != actor.tenantID || !Authorize(actor.subject, ActionAddMember, tenantTarget(actor.tenantID)).Allowed {
		return Membership{}, ErrForbidden
	}
	returnMember := Membership{}
	now := s.now().UTC()
	_, err := database.InTenantTx(ctx, s.pools.Tenant, actor.tenantID, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if err := s.ensureActiveTenant(ctx, tx, actor.tenantID); err != nil {
			return struct{}{}, err
		}
		id, err := s.newID()
		if err != nil {
			return struct{}{}, ErrStorage
		}
		row, err := createMembership(ctx, tx, id, actor.tenantID, command.effectiveUserID(), now)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		returnMember = mapMembership(row)
		if err := s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &actor.tenantID, "tenancy.membership.added", "membership", returnMember.ID, "success", map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": returnMember.ID.String(), "identity_id": returnMember.UserID.String(),
		}, audit.OutboxEvent{EventType: "tenancy.membership.added", AggregateType: "membership", AggregateID: returnMember.ID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": returnMember.ID.String(),
		}, AvailableAt: now}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil {
		return Membership{}, err
	}
	return returnMember, nil
}

func (s *Service) RemoveMember(ctx context.Context, actor TenantActor, membershipID uuid.UUID) error {
	if membershipID == uuid.Nil {
		return ErrInvalidArgument
	}
	if !Authorize(actor.subject, ActionRemoveMember, tenantTarget(actor.tenantID)).Allowed {
		return ErrForbidden
	}
	now := s.now().UTC()
	_, err := database.InTenantTx(ctx, s.pools.Tenant, actor.tenantID, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if err := s.ensureActiveTenant(ctx, tx, actor.tenantID); err != nil {
			return struct{}{}, err
		}
		membership, err := getMembership(ctx, tx, membershipID)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		adminForMembership, err := countTenantAdminBindingsForMembership(ctx, tx, actor.tenantID, membership.ID)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if adminForMembership > 0 {
			totalAdmins, err := countTenantAdminBindings(ctx, tx, actor.tenantID)
			if err != nil {
				return struct{}{}, mapStorageError(err)
			}
			if totalAdmins <= 1 {
				return struct{}{}, ErrForbidden
			}
		}
		rows, err := deleteMembership(ctx, tx, membershipID)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if rows != 1 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &actor.tenantID, "tenancy.membership.removed", "membership", membershipID, "success", map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": membershipID.String(),
		}, audit.OutboxEvent{EventType: "tenancy.membership.removed", AggregateType: "membership", AggregateID: membershipID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": membershipID.String(),
		}, AvailableAt: now})
	})
	return err
}

func (s *Service) GrantRole(ctx context.Context, actor TenantActor, command GrantRole) (RoleBinding, error) {
	if err := command.Validate(); err != nil {
		return RoleBinding{}, err
	}
	if command.TenantID != actor.tenantID || !Authorize(actor.subject, ActionGrantRole, tenantTarget(actor.tenantID)).Allowed {
		return RoleBinding{}, ErrForbidden
	}
	now := s.now().UTC()
	var result RoleBinding
	_, err := database.InTenantTx(ctx, s.pools.Tenant, actor.tenantID, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if err := s.ensureActiveTenant(ctx, tx, actor.tenantID); err != nil {
			return struct{}{}, err
		}
		membership, err := getMembership(ctx, tx, command.MembershipID)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if membership.TenantID != actor.tenantID {
			return struct{}{}, ErrNotFound
		}
		id, err := s.newID()
		if err != nil {
			return struct{}{}, ErrStorage
		}
		row, err := createRoleBinding(ctx, tx, id, actor.tenantID, command.MembershipID, command.Role, now)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		result = mapRoleBinding(row)
		return struct{}{}, s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &actor.tenantID, "tenancy.role.granted", "role_binding", result.ID, "success", map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": command.MembershipID.String(), "role": string(command.Role),
		}, audit.OutboxEvent{EventType: "tenancy.role.granted", AggregateType: "role_binding", AggregateID: result.ID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": command.MembershipID.String(), "role": string(command.Role),
		}, AvailableAt: now})
	})
	if err != nil {
		return RoleBinding{}, err
	}
	return result, nil
}

func (s *Service) RevokeRole(ctx context.Context, actor TenantActor, bindingID uuid.UUID) error {
	if bindingID == uuid.Nil {
		return ErrInvalidArgument
	}
	if !Authorize(actor.subject, ActionRevokeRole, tenantTarget(actor.tenantID)).Allowed {
		return ErrForbidden
	}
	now := s.now().UTC()
	_, err := database.InTenantTx(ctx, s.pools.Tenant, actor.tenantID, actor.ActorID(), func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		if err := s.ensureActiveTenant(ctx, tx, actor.tenantID); err != nil {
			return struct{}{}, err
		}
		binding, err := getRoleBinding(ctx, tx, bindingID)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if Role(binding.Role) == TenantAdmin {
			totalAdmins, err := countTenantAdminBindings(ctx, tx, actor.tenantID)
			if err != nil {
				return struct{}{}, mapStorageError(err)
			}
			if totalAdmins <= 1 {
				return struct{}{}, ErrForbidden
			}
		}
		rows, err := deleteRoleBinding(ctx, tx, bindingID)
		if err != nil {
			return struct{}{}, mapStorageError(err)
		}
		if rows != 1 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, s.appendAudit(ctx, tx, actor.metadata, actor.ActorID(), &actor.tenantID, "tenancy.role.revoked", "role_binding", bindingID, "success", map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": binding.MembershipID.String(), "role": binding.Role,
		}, audit.OutboxEvent{EventType: "tenancy.role.revoked", AggregateType: "role_binding", AggregateID: bindingID, PayloadVersion: 1, Payload: map[string]any{
			"tenant_id": actor.tenantID.String(), "membership_id": binding.MembershipID.String(), "role": binding.Role,
		}, AvailableAt: now})
	})
	return err
}

func (s *Service) ensureActiveTenant(ctx context.Context, executor dbgen.DBTX, tenantID uuid.UUID) error {
	if err := lockTenant(ctx, executor, tenantID); err != nil {
		return mapStorageError(err)
	}
	row, err := getTenant(ctx, executor, tenantID)
	if err != nil {
		return mapStorageError(err)
	}
	if TenantState(row.State) != Active {
		return ErrTenantSuspended
	}
	return nil
}

func (s *Service) appendAudit(ctx context.Context, executor database.Executor, metadata ActorMetadata, actorID uuid.UUID, tenantID *uuid.UUID, action, resourceType string, resourceID uuid.UUID, result string, details map[string]any, outbox audit.OutboxEvent) error {
	actor := actorID
	resource := resourceID
	return s.audit.Append(ctx, executor, audit.Event{
		ActorType: metadata.ActorType, ActorID: &actor, TenantID: tenantID, Action: action,
		ResourceType: resourceType, ResourceID: &resource, Result: result, SourceIP: metadata.SourceIP,
		UserAgent: metadata.UserAgent, RequestID: metadata.RequestID, Details: details,
	}, outbox)
}

func tenantTarget(tenantID uuid.UUID) Target { return Target{Scope: ScopeTenant, TenantID: tenantID} }

func mapTenant(row dbgen.TenancyTenant) Tenant {
	return Tenant{ID: row.ID, Slug: row.Slug, Name: row.Name, State: TenantState(row.State), Version: row.Version, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
}

func mapMembership(row dbgen.TenancyMembership) Membership {
	return Membership{ID: row.ID, TenantID: row.TenantID, UserID: row.IdentityID, Version: row.Version, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
}

func mapRoleBinding(row dbgen.TenancyRoleBinding) RoleBinding {
	return RoleBinding{ID: row.ID, TenantID: row.TenantID, MembershipID: row.MembershipID, Role: Role(row.Role), Version: row.Version, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
}
