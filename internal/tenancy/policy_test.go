package tenancy_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
)

func TestAuthorizeUsesFixedRoleAndScopeMatrix(t *testing.T) {
	tenantID := uuid.New()
	otherTenantID := uuid.New()
	platformSubject := tenancy.Subject{
		ActorID:       uuid.New(),
		PlatformRoles: []tenancy.Role{tenancy.PlatformAdmin},
	}
	tenantAdminSubject := tenancy.Subject{
		ActorID: uuid.New(),
		TenantGrants: []tenancy.TenantGrant{
			{TenantID: tenantID, Role: tenancy.TenantAdmin},
		},
	}

	tests := []struct {
		name    string
		subject tenancy.Subject
		action  tenancy.Action
		target  tenancy.Target
		allowed bool
	}{
		{name: "platform_create", subject: platformSubject, action: tenancy.ActionCreateTenant, target: platformTarget(), allowed: true},
		{name: "platform_update", subject: platformSubject, action: tenancy.ActionUpdateTenant, target: platformTarget(), allowed: true},
		{name: "platform_cannot_add_member", subject: platformSubject, action: tenancy.ActionAddMember, target: tenantTarget(tenantID)},
		{name: "platform_cannot_grant_role", subject: platformSubject, action: tenancy.ActionGrantRole, target: tenantTarget(tenantID)},
		{name: "tenant_admin_add_member", subject: tenantAdminSubject, action: tenancy.ActionAddMember, target: tenantTarget(tenantID), allowed: true},
		{name: "tenant_admin_remove_member", subject: tenantAdminSubject, action: tenancy.ActionRemoveMember, target: tenantTarget(tenantID), allowed: true},
		{name: "tenant_admin_grant_role", subject: tenantAdminSubject, action: tenancy.ActionGrantRole, target: tenantTarget(tenantID), allowed: true},
		{name: "tenant_admin_revoke_role", subject: tenantAdminSubject, action: tenancy.ActionRevokeRole, target: tenantTarget(tenantID), allowed: true},
		{name: "tenant_admin_cannot_create_tenant", subject: tenantAdminSubject, action: tenancy.ActionCreateTenant, target: platformTarget()},
		{name: "tenant_admin_cannot_cross_tenant", subject: tenantAdminSubject, action: tenancy.ActionAddMember, target: tenantTarget(otherTenantID)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := tenancy.Authorize(tt.subject, tt.action, tt.target)
			if decision.Allowed != tt.allowed {
				t.Fatalf("Authorize() allowed = %t, want %t; reason=%q", decision.Allowed, tt.allowed, decision.Reason)
			}
			if !decision.Allowed && decision.Reason == "" {
				t.Fatal("Authorize() denied without a stable reason")
			}
		})
	}
}

func TestAuthorizeDoesNotApplyRolesAsTenantCartesianProduct(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	subject := tenancy.Subject{
		ActorID: uuid.New(),
		TenantGrants: []tenancy.TenantGrant{
			{TenantID: tenantA, Role: tenancy.TenantAdmin},
			{TenantID: tenantB, Role: tenancy.Consumer},
		},
	}

	if decision := tenancy.Authorize(subject, tenancy.ActionAddMember, tenantTarget(tenantB)); decision.Allowed {
		t.Fatalf("Tenant A administrator role expanded to Tenant B: %#v", decision)
	}
	if decision := tenancy.Authorize(subject, tenancy.ActionAddMember, tenantTarget(tenantA)); !decision.Allowed {
		t.Fatalf("Tenant A administrator was denied its own tenant: %#v", decision)
	}
}

func TestAuthorizeDefaultsToDenyForUnknownOrIncompleteInput(t *testing.T) {
	tenantID := uuid.New()
	validSubject := tenancy.Subject{
		ActorID: uuid.New(),
		TenantGrants: []tenancy.TenantGrant{
			{TenantID: tenantID, Role: tenancy.TenantAdmin},
		},
	}

	tests := []struct {
		name    string
		subject tenancy.Subject
		action  tenancy.Action
		target  tenancy.Target
	}{
		{name: "zero_actor", subject: tenancy.Subject{TenantGrants: validSubject.TenantGrants}, action: tenancy.ActionAddMember, target: tenantTarget(tenantID)},
		{name: "no_roles", subject: tenancy.Subject{ActorID: uuid.New()}, action: tenancy.ActionAddMember, target: tenantTarget(tenantID)},
		{name: "unknown_action", subject: validSubject, action: tenancy.Action("unknown"), target: tenantTarget(tenantID)},
		{name: "unknown_role", subject: tenancy.Subject{ActorID: uuid.New(), TenantGrants: []tenancy.TenantGrant{{TenantID: tenantID, Role: tenancy.Role("owner")}}}, action: tenancy.ActionAddMember, target: tenantTarget(tenantID)},
		{name: "zero_grant_tenant", subject: tenancy.Subject{ActorID: uuid.New(), TenantGrants: []tenancy.TenantGrant{{Role: tenancy.TenantAdmin}}}, action: tenancy.ActionAddMember, target: tenantTarget(tenantID)},
		{name: "unknown_scope", subject: validSubject, action: tenancy.ActionAddMember, target: tenancy.Target{Scope: tenancy.Scope("unknown"), TenantID: tenantID}},
		{name: "tenant_scope_without_tenant", subject: validSubject, action: tenancy.ActionAddMember, target: tenancy.Target{Scope: tenancy.ScopeTenant}},
		{name: "platform_scope_with_tenant", subject: validSubject, action: tenancy.ActionCreateTenant, target: tenancy.Target{Scope: tenancy.ScopePlatform, TenantID: tenantID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := tenancy.Authorize(tt.subject, tt.action, tt.target)
			if decision.Allowed {
				t.Fatalf("Authorize() allowed invalid input: %#v", decision)
			}
			if decision.Reason == "" {
				t.Fatal("Authorize() denied invalid input without a reason")
			}
		})
	}
}

func TestNonAdministrativeTenantRolesCannotPerformTaskFourWrites(t *testing.T) {
	tenantID := uuid.New()
	actions := []tenancy.Action{
		tenancy.ActionAddMember,
		tenancy.ActionRemoveMember,
		tenancy.ActionGrantRole,
		tenancy.ActionRevokeRole,
	}
	roles := []tenancy.Role{tenancy.Operator, tenancy.Auditor, tenancy.Consumer}

	for _, role := range roles {
		for _, action := range actions {
			t.Run(string(role)+"/"+string(action), func(t *testing.T) {
				subject := tenancy.Subject{
					ActorID: uuid.New(),
					TenantGrants: []tenancy.TenantGrant{
						{TenantID: tenantID, Role: role},
					},
				}
				if decision := tenancy.Authorize(subject, action, tenantTarget(tenantID)); decision.Allowed {
					t.Fatalf("role %q unexpectedly authorized for %q", role, action)
				}
			})
		}
	}
}

func TestActorConstructorsEnforcePlatformAndRouteTenantBoundaries(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	metadata := validActorMetadata()
	platformSubject := tenancy.Subject{ActorID: uuid.New(), PlatformRoles: []tenancy.Role{tenancy.PlatformAdmin}}
	tenantSubject := tenancy.Subject{
		ActorID: uuid.New(),
		TenantGrants: []tenancy.TenantGrant{
			{TenantID: tenantA, Role: tenancy.TenantAdmin},
		},
	}

	if _, err := tenancy.NewPlatformActor(platformSubject, metadata); err != nil {
		t.Fatalf("NewPlatformActor() rejected platform administrator: %v", err)
	}
	if _, err := tenancy.NewPlatformActor(tenantSubject, metadata); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("NewPlatformActor() tenant-only error = %v, want ErrForbidden", err)
	}
	if _, err := tenancy.NewTenantActor(tenantSubject, tenantA, metadata); err != nil {
		t.Fatalf("NewTenantActor() rejected matching route tenant: %v", err)
	}
	if _, err := tenancy.NewTenantActor(tenantSubject, tenantB, metadata); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("NewTenantActor() route mismatch error = %v, want ErrForbidden", err)
	}
	if _, err := tenancy.NewTenantActor(platformSubject, tenantA, metadata); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("NewTenantActor() platform-only error = %v, want ErrForbidden", err)
	}
}

func TestTenantActorAuthorizationCannotBeMutatedAfterConstruction(t *testing.T) {
	tenantID := uuid.New()
	grants := []tenancy.TenantGrant{{TenantID: tenantID, Role: tenancy.Consumer}}
	actor, err := tenancy.NewTenantActor(tenancy.Subject{ActorID: uuid.New(), TenantGrants: grants}, tenantID, validActorMetadata())
	if err != nil {
		t.Fatalf("NewTenantActor() error = %v", err)
	}

	grants[0].Role = tenancy.TenantAdmin
	service := tenancy.NewService(nil, nil)
	_, err = service.AddMember(t.Context(), actor, tenancy.AddMember{TenantID: tenantID, IdentityID: uuid.New()})
	if !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("AddMember() after caller mutated grants error = %v, want ErrForbidden", err)
	}
}

func platformTarget() tenancy.Target {
	return tenancy.Target{Scope: tenancy.ScopePlatform}
}

func tenantTarget(tenantID uuid.UUID) tenancy.Target {
	return tenancy.Target{Scope: tenancy.ScopeTenant, TenantID: tenantID}
}

func validActorMetadata() tenancy.ActorMetadata {
	return tenancy.ActorMetadata{
		ActorType: "user",
		SourceIP:  netip.MustParseAddr("203.0.113.30"),
		UserAgent: "tenancy-policy-test/1.0",
		RequestID: uuid.New(),
	}
}
