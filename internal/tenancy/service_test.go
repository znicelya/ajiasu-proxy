package tenancy_test

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
)

func TestActorConstructorsRejectInvalidMetadataBeforeUse(t *testing.T) {
	tenantID := uuid.New()
	platformSubject := tenancy.Subject{ActorID: uuid.New(), PlatformRoles: []tenancy.Role{tenancy.PlatformAdmin}}
	tenantSubject := tenancy.Subject{
		ActorID: uuid.New(),
		TenantGrants: []tenancy.TenantGrant{
			{TenantID: tenantID, Role: tenancy.TenantAdmin},
		},
	}

	tests := []struct {
		name   string
		mutate func(*tenancy.ActorMetadata)
	}{
		{name: "actor_type", mutate: func(metadata *tenancy.ActorMetadata) { metadata.ActorType = "" }},
		{name: "source_ip", mutate: func(metadata *tenancy.ActorMetadata) { metadata.SourceIP = netip.Addr{} }},
		{name: "user_agent", mutate: func(metadata *tenancy.ActorMetadata) { metadata.UserAgent = "" }},
		{name: "request_id", mutate: func(metadata *tenancy.ActorMetadata) { metadata.RequestID = uuid.Nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := validActorMetadata()
			tt.mutate(&metadata)
			if _, err := tenancy.NewPlatformActor(platformSubject, metadata); !errors.Is(err, tenancy.ErrInvalidArgument) {
				t.Fatalf("NewPlatformActor() error = %v, want ErrInvalidArgument", err)
			}
			if _, err := tenancy.NewTenantActor(tenantSubject, tenantID, metadata); !errors.Is(err, tenancy.ErrInvalidArgument) {
				t.Fatalf("NewTenantActor() error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestTenantStatesAndTransitionsAreClosed(t *testing.T) {
	for _, state := range []tenancy.TenantState{tenancy.Active, tenancy.Suspended, tenancy.Deleting} {
		if !state.Valid() {
			t.Fatalf("fixed tenant state %q is invalid", state)
		}
	}
	if state := tenancy.TenantState("disabled"); state.Valid() {
		t.Fatalf("unknown tenant state %q is valid", state)
	}

	allowed := []struct {
		from tenancy.TenantState
		to   tenancy.TenantState
	}{
		{from: tenancy.Active, to: tenancy.Suspended},
		{from: tenancy.Active, to: tenancy.Deleting},
		{from: tenancy.Suspended, to: tenancy.Active},
		{from: tenancy.Suspended, to: tenancy.Deleting},
	}
	for _, transition := range allowed {
		if err := tenancy.ValidateTenantStateTransition(transition.from, transition.to); err != nil {
			t.Fatalf("transition %q -> %q rejected: %v", transition.from, transition.to, err)
		}
	}

	for _, target := range []tenancy.TenantState{tenancy.Active, tenancy.Suspended} {
		if err := tenancy.ValidateTenantStateTransition(tenancy.Deleting, target); !errors.Is(err, tenancy.ErrInvalidArgument) {
			t.Fatalf("deleting -> %q error = %v, want ErrInvalidArgument", target, err)
		}
	}
}

func TestCreateTenantValidation(t *testing.T) {
	if err := (tenancy.CreateTenant{Name: " Acme Operations ", InitialAdminIdentityID: uuid.New()}).Validate(); err != nil {
		t.Fatalf("valid CreateTenant rejected: %v", err)
	}
	for _, name := range []string{"", "   "} {
		if err := (tenancy.CreateTenant{Name: name, InitialAdminIdentityID: uuid.New()}).Validate(); !errors.Is(err, tenancy.ErrInvalidArgument) {
			t.Fatalf("CreateTenant name %q error = %v, want ErrInvalidArgument", name, err)
		}
	}
	if err := (tenancy.CreateTenant{Name: "No Initial Admin"}).Validate(); !errors.Is(err, tenancy.ErrInvalidArgument) {
		t.Fatalf("CreateTenant without initial admin error = %v, want ErrInvalidArgument", err)
	}
}

func TestUpdateTenantValidationRequiresTargetVersionAndPatch(t *testing.T) {
	tenantID := uuid.New()
	name := "Updated Tenant"
	state := tenancy.Suspended
	valid := tenancy.UpdateTenant{TenantID: tenantID, ExpectedVersion: 3, Name: &name, State: &state}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid UpdateTenant rejected: %v", err)
	}

	tests := []struct {
		name   string
		update tenancy.UpdateTenant
	}{
		{name: "tenant_id", update: tenancy.UpdateTenant{ExpectedVersion: 1, Name: &name}},
		{name: "version_zero", update: tenancy.UpdateTenant{TenantID: tenantID, Name: &name}},
		{name: "version_negative", update: tenancy.UpdateTenant{TenantID: tenantID, ExpectedVersion: -1, Name: &name}},
		{name: "empty_patch", update: tenancy.UpdateTenant{TenantID: tenantID, ExpectedVersion: 1}},
		{name: "blank_name", update: tenancy.UpdateTenant{TenantID: tenantID, ExpectedVersion: 1, Name: stringPointer("   ")}},
		{name: "unknown_state", update: tenancy.UpdateTenant{TenantID: tenantID, ExpectedVersion: 1, State: statePointer(tenancy.TenantState("disabled"))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.update.Validate(); !errors.Is(err, tenancy.ErrInvalidArgument) {
				t.Fatalf("UpdateTenant.Validate() error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestMemberAndRoleCommandValidation(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	otherUserID := uuid.New()
	membershipID := uuid.New()
	if err := (tenancy.AddMember{TenantID: tenantID, UserID: userID}).Validate(); err != nil {
		t.Fatalf("valid AddMember rejected: %v", err)
	}
	if err := (tenancy.GrantRole{TenantID: tenantID, MembershipID: membershipID, Role: tenancy.TenantAdmin}).Validate(); err != nil {
		t.Fatalf("valid GrantRole rejected: %v", err)
	}

	invalidMembers := []tenancy.AddMember{
		{UserID: userID},
		{TenantID: tenantID},
		{TenantID: tenantID, UserID: userID, IdentityID: otherUserID},
	}
	for _, command := range invalidMembers {
		if err := command.Validate(); !errors.Is(err, tenancy.ErrInvalidArgument) {
			t.Fatalf("AddMember.Validate() error = %v, want ErrInvalidArgument", err)
		}
	}

	invalidRoles := []tenancy.GrantRole{
		{MembershipID: membershipID, Role: tenancy.TenantAdmin},
		{TenantID: tenantID, Role: tenancy.TenantAdmin},
		{TenantID: tenantID, MembershipID: membershipID, Role: tenancy.PlatformAdmin},
		{TenantID: tenantID, MembershipID: membershipID, Role: tenancy.Role("owner")},
	}
	for _, command := range invalidRoles {
		if err := command.Validate(); !errors.Is(err, tenancy.ErrInvalidArgument) {
			t.Fatalf("GrantRole.Validate() error = %v, want ErrInvalidArgument", err)
		}
	}
}

func TestStableTenancyErrorsSupportErrorsIs(t *testing.T) {
	errorsToCheck := []error{
		tenancy.ErrForbidden,
		tenancy.ErrNotFound,
		tenancy.ErrVersionConflict,
		tenancy.ErrTenantSuspended,
		tenancy.ErrAlreadyExists,
		tenancy.ErrInvalidArgument,
	}
	for _, target := range errorsToCheck {
		if target == nil {
			t.Fatal("stable tenancy error is nil")
		}
		if !errors.Is(fmt.Errorf("wrapped: %w", target), target) {
			t.Fatalf("wrapped tenancy error does not match %v", target)
		}
	}
}

func stringPointer(value string) *string {
	return &value
}

func statePointer(value tenancy.TenantState) *tenancy.TenantState {
	return &value
}
