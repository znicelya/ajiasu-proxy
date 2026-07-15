package tenancy

import (
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden       = errors.New("tenancy operation is forbidden")
	ErrNotFound        = errors.New("tenancy resource was not found")
	ErrVersionConflict = errors.New("tenancy resource version conflict")
	ErrTenantSuspended = errors.New("tenant does not accept operations")
	ErrAlreadyExists   = errors.New("tenancy resource already exists")
	ErrInvalidArgument = errors.New("invalid tenancy argument")
	ErrStorage         = errors.New("tenancy storage failure")
)

type Role string

const (
	PlatformAdmin Role = "platform_admin"
	TenantAdmin   Role = "tenant_admin"
	Operator      Role = "operator"
	Auditor       Role = "auditor"
	Consumer      Role = "consumer"
)

func (r Role) Valid() bool {
	switch r {
	case PlatformAdmin, TenantAdmin, Operator, Auditor, Consumer:
		return true
	default:
		return false
	}
}

func (r Role) TenantScoped() bool {
	return r == TenantAdmin || r == Operator || r == Auditor || r == Consumer
}

type TenantState string

const (
	Active    TenantState = "active"
	Suspended TenantState = "suspended"
	Deleting  TenantState = "deleting"
)

func (s TenantState) Valid() bool {
	return s == Active || s == Suspended || s == Deleting
}

func ValidateTenantStateTransition(from, to TenantState) error {
	if !from.Valid() || !to.Valid() || from == to {
		return ErrInvalidArgument
	}
	if from == Deleting {
		return ErrInvalidArgument
	}
	return nil
}

type Tenant struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	State     TenantState
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Membership struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RoleBinding struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	MembershipID uuid.UUID
	Role         Role
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateTenant struct {
	Slug                   string
	Name                   string
	InitialAdminIdentityID uuid.UUID
}

func (c CreateTenant) Validate() error {
	if c.InitialAdminIdentityID == uuid.Nil {
		return ErrInvalidArgument
	}
	if err := validateName(c.Name); err != nil {
		return err
	}
	if c.Slug != "" && !validSlug(c.Slug) {
		return ErrInvalidArgument
	}
	return nil
}

type UpdateTenant struct {
	TenantID        uuid.UUID
	ExpectedVersion int64
	Name            *string
	State           *TenantState
}

func (u UpdateTenant) Validate() error {
	if u.TenantID == uuid.Nil || u.ExpectedVersion <= 0 || (u.Name == nil && u.State == nil) {
		return ErrInvalidArgument
	}
	if u.Name != nil {
		if err := validateName(*u.Name); err != nil {
			return err
		}
	}
	if u.State != nil && !u.State.Valid() {
		return ErrInvalidArgument
	}
	return nil
}

type AddMember struct {
	TenantID uuid.UUID
	// UserID is the domain name; IdentityID is retained as a migration/API alias.
	UserID     uuid.UUID
	IdentityID uuid.UUID
}

func (a AddMember) Validate() error {
	if a.TenantID == uuid.Nil || a.effectiveUserID() == uuid.Nil {
		return ErrInvalidArgument
	}
	if a.UserID != uuid.Nil && a.IdentityID != uuid.Nil && a.UserID != a.IdentityID {
		return ErrInvalidArgument
	}
	return nil
}

func (a AddMember) effectiveUserID() uuid.UUID {
	if a.UserID != uuid.Nil {
		return a.UserID
	}
	return a.IdentityID
}

type GrantRole struct {
	TenantID     uuid.UUID
	MembershipID uuid.UUID
	Role         Role
}

func (g GrantRole) Validate() error {
	if g.TenantID == uuid.Nil || g.MembershipID == uuid.Nil || !g.Role.TenantScoped() {
		return ErrInvalidArgument
	}
	return nil
}

type ActorMetadata struct {
	ActorType string
	SourceIP  netip.Addr
	UserAgent string
	RequestID uuid.UUID
}

func (m ActorMetadata) validate() error {
	if strings.TrimSpace(m.ActorType) == "" || len(strings.TrimSpace(m.ActorType)) > 64 {
		return ErrInvalidArgument
	}
	if !m.SourceIP.IsValid() || m.SourceIP.Zone() != "" {
		return ErrInvalidArgument
	}
	if strings.TrimSpace(m.UserAgent) == "" || len(strings.TrimSpace(m.UserAgent)) > 1024 {
		return ErrInvalidArgument
	}
	if m.RequestID == uuid.Nil {
		return ErrInvalidArgument
	}
	return nil
}

type PlatformActor struct {
	subject  Subject
	metadata ActorMetadata
}

type TenantActor struct {
	subject  Subject
	tenantID uuid.UUID
	metadata ActorMetadata
}

func (a PlatformActor) ActorID() uuid.UUID    { return a.subject.ActorID }
func (a TenantActor) ActorID() uuid.UUID      { return a.subject.ActorID }
func (a TenantActor) TenantID() uuid.UUID     { return a.tenantID }
func (a TenantActor) Metadata() ActorMetadata { return a.metadata }
func (a TenantActor) Allows(action Action) bool {
	return Authorize(a.subject, action, tenantTarget(a.tenantID)).Allowed
}

func NewPlatformActor(subject Subject, metadata ActorMetadata) (PlatformActor, error) {
	if err := metadata.validate(); err != nil {
		return PlatformActor{}, err
	}
	if !Authorize(subject, ActionCreateTenant, Target{Scope: ScopePlatform}).Allowed {
		return PlatformActor{}, ErrForbidden
	}
	return PlatformActor{subject: cloneSubject(subject), metadata: normalizeMetadata(metadata)}, nil
}

func NewTenantActor(subject Subject, routeTenantID uuid.UUID, metadata ActorMetadata) (TenantActor, error) {
	if err := metadata.validate(); err != nil {
		return TenantActor{}, err
	}
	if routeTenantID == uuid.Nil || !subjectHasTenantGrant(subject, routeTenantID) {
		return TenantActor{}, ErrForbidden
	}
	return TenantActor{subject: cloneSubject(subject), tenantID: routeTenantID, metadata: normalizeMetadata(metadata)}, nil
}

func cloneSubject(subject Subject) Subject {
	subject.PlatformRoles = append([]Role(nil), subject.PlatformRoles...)
	subject.TenantGrants = append([]TenantGrant(nil), subject.TenantGrants...)
	return subject
}

func normalizeMetadata(metadata ActorMetadata) ActorMetadata {
	metadata.ActorType = strings.TrimSpace(metadata.ActorType)
	metadata.SourceIP = metadata.SourceIP.Unmap()
	metadata.UserAgent = strings.TrimSpace(metadata.UserAgent)
	return metadata
}

func validateName(value string) error {
	length := len(strings.TrimSpace(value))
	if length == 0 || length > 200 {
		return ErrInvalidArgument
	}
	return nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validSlug(value string) bool {
	return slugPattern.MatchString(strings.TrimSpace(value))
}

func normalizeSlug(command CreateTenant) string {
	if command.Slug != "" {
		return strings.TrimSpace(command.Slug)
	}
	value := strings.ToLower(strings.TrimSpace(command.Name))
	var builder strings.Builder
	lastHyphen := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
			lastHyphen = false
		} else if !lastHyphen && builder.Len() > 0 {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
