package accounts

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden             = errors.New("account operation is forbidden")
	ErrInvalidArgument       = errors.New("invalid account argument")
	ErrNotFound              = errors.New("account was not found")
	ErrAlreadyExists         = errors.New("account already exists")
	ErrVersionConflict       = errors.New("account version conflict")
	ErrQuotaExceeded         = errors.New("account quota exceeded")
	ErrCapacityExhausted     = errors.New("account capacity exhausted")
	ErrDependencyUnavailable = errors.New("secret dependency unavailable")
	ErrStorage               = errors.New("account storage failure")
)

type State string

const (
	StateActive   State = "active"
	StateDisabled State = "disabled"
	StateDeleting State = "deleting"
)

type Health string

const (
	HealthUnknown     Health = "unknown"
	HealthHealthy     Health = "healthy"
	HealthDegraded    Health = "degraded"
	HealthUnhealthy   Health = "unhealthy"
	HealthQuarantined Health = "quarantined"
)

type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type CredentialMetadata struct {
	Version   int64      `json:"version"`
	Provider  string     `json:"provider"`
	CreatedAt time.Time  `json:"created_at"`
	RetiredAt *time.Time `json:"retired_at,omitempty"`
}

type Account struct {
	ID                  uuid.UUID          `json:"id"`
	TenantID            uuid.UUID          `json:"tenant_id"`
	Name                string             `json:"name"`
	State               State              `json:"state"`
	Health              Health             `json:"health"`
	MembershipID        *string            `json:"membership_id,omitempty"`
	MembershipExpiresAt *time.Time         `json:"membership_expires_at,omitempty"`
	Labels              map[string]string  `json:"labels"`
	MaxConcurrency      int                `json:"max_concurrency"`
	Version             int64              `json:"version"`
	Credential          CredentialMetadata `json:"credential"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

type CreateCommand struct {
	Name                string            `json:"name"`
	MembershipID        *string           `json:"membership_id,omitempty"`
	MembershipExpiresAt *time.Time        `json:"membership_expires_at,omitempty"`
	Labels              map[string]string `json:"labels,omitempty"`
	MaxConcurrency      int               `json:"max_concurrency,omitempty"`
	Credential          Credential        `json:"credential"`
}

type UpdateCommand struct {
	ExpectedVersion     int64              `json:"expected_version"`
	Name                *string            `json:"name,omitempty"`
	State               *State             `json:"state,omitempty"`
	MembershipID        *string            `json:"membership_id,omitempty"`
	MembershipExpiresAt *time.Time         `json:"membership_expires_at,omitempty"`
	Labels              *map[string]string `json:"labels,omitempty"`
	MaxConcurrency      *int               `json:"max_concurrency,omitempty"`
}

type BulkResult struct {
	Index     int        `json:"index"`
	AccountID *uuid.UUID `json:"account_id,omitempty"`
	Code      string     `json:"code"`
	Message   string     `json:"message"`
}

type CapacityReservation struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	OwnerID   uuid.UUID
	ExpiresAt time.Time
}

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,62})$`)

func validateCreate(c CreateCommand) error {
	if len(strings.TrimSpace(c.Name)) == 0 || len(strings.TrimSpace(c.Name)) > 200 {
		return ErrInvalidArgument
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 1
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 32 || len(c.Credential.Username) == 0 || len(c.Credential.Username) > 512 || len(c.Credential.Password) == 0 || len(c.Credential.Password) > 8192 {
		return ErrInvalidArgument
	}
	if c.MembershipID != nil && (len(strings.TrimSpace(*c.MembershipID)) == 0 || len(strings.TrimSpace(*c.MembershipID)) > 512) {
		return ErrInvalidArgument
	}
	return validateLabels(c.Labels)
}

func validateLabels(labels map[string]string) error {
	if len(labels) > 64 {
		return ErrInvalidArgument
	}
	for key, value := range labels {
		if !labelPattern.MatchString(key) || len(value) > 256 {
			return ErrInvalidArgument
		}
	}
	return nil
}

func validState(value State) bool {
	return value == StateActive || value == StateDisabled || value == StateDeleting
}
