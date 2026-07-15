package tenancy

import "github.com/google/uuid"

type TenantGrant struct {
	TenantID uuid.UUID
	Role     Role
}

type Subject struct {
	ActorID       uuid.UUID
	PlatformRoles []Role
	TenantGrants  []TenantGrant
}

type Action string

const (
	ActionCreateTenant     Action = "create_tenant"
	ActionUpdateTenant     Action = "update_tenant"
	ActionAddMember        Action = "add_member"
	ActionRemoveMember     Action = "remove_member"
	ActionGrantRole        Action = "grant_role"
	ActionRevokeRole       Action = "revoke_role"
	ActionReadResources    Action = "read_tenant_resources"
	ActionManageResources  Action = "manage_tenant_resources"
	ActionOperateResources Action = "operate_tenant_resources"
	ActionManageQuota      Action = "manage_tenant_quota"
	ActionReadNodes        Action = "read_nodes"
	ActionManageNodes      Action = "manage_nodes"
	ActionReadPlatformOps  Action = "read_platform_operations"
)

type Scope string

const (
	ScopePlatform Scope = "platform"
	ScopeTenant   Scope = "tenant"
)

type Target struct {
	Scope    Scope
	TenantID uuid.UUID
}

type Decision struct {
	Allowed bool
	Reason  string
}

func Authorize(subject Subject, action Action, target Target) Decision {
	if subject.ActorID == uuid.Nil {
		return deny("invalid_actor")
	}
	if !validSubjectRoles(subject) {
		return deny("invalid_role")
	}
	switch target.Scope {
	case ScopePlatform:
		if target.TenantID != uuid.Nil {
			return deny("invalid_scope")
		}
		if action != ActionCreateTenant && action != ActionUpdateTenant && action != ActionReadNodes && action != ActionManageNodes && action != ActionReadPlatformOps {
			return deny("action_scope_mismatch")
		}
		for _, role := range subject.PlatformRoles {
			if role == PlatformAdmin {
				return Decision{Allowed: true, Reason: "platform_admin"}
			}
		}
		return deny("insufficient_role")
	case ScopeTenant:
		if target.TenantID == uuid.Nil {
			return deny("invalid_scope")
		}
		switch action {
		case ActionAddMember, ActionRemoveMember, ActionGrantRole, ActionRevokeRole, ActionReadResources, ActionManageResources, ActionOperateResources, ActionManageQuota:
		default:
			return deny("action_scope_mismatch")
		}
		for _, grant := range subject.TenantGrants {
			if grant.TenantID != target.TenantID {
				continue
			}
			if action == ActionReadResources && (grant.Role == TenantAdmin || grant.Role == Operator || grant.Role == Auditor) {
				return Decision{Allowed: true, Reason: string(grant.Role)}
			}
			if action == ActionOperateResources && (grant.Role == TenantAdmin || grant.Role == Operator) {
				return Decision{Allowed: true, Reason: string(grant.Role)}
			}
			if grant.Role == TenantAdmin {
				return Decision{Allowed: true, Reason: "tenant_admin"}
			}
		}
		return deny("insufficient_role")
	default:
		return deny("invalid_scope")
	}
}

func validSubjectRoles(subject Subject) bool {
	for _, role := range subject.PlatformRoles {
		if role != PlatformAdmin {
			return false
		}
	}
	for _, grant := range subject.TenantGrants {
		if grant.TenantID == uuid.Nil || !grant.Role.TenantScoped() {
			return false
		}
	}
	return true
}

func subjectHasTenantGrant(subject Subject, tenantID uuid.UUID) bool {
	if subject.ActorID == uuid.Nil || !validSubjectRoles(subject) {
		return false
	}
	for _, grant := range subject.TenantGrants {
		if grant.TenantID == tenantID {
			return true
		}
	}
	return false
}

func deny(reason string) Decision {
	return Decision{Reason: reason}
}
