package pools

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden        = errors.New("pool operation is forbidden")
	ErrInvalidArgument  = errors.New("invalid pool argument")
	ErrNotFound         = errors.New("pool was not found")
	ErrAlreadyExists    = errors.New("pool already exists")
	ErrVersionConflict  = errors.New("pool version conflict")
	ErrQuotaExceeded    = errors.New("pool quota exceeded")
	ErrSelectorMismatch = errors.New("account labels do not match pool selector")
	ErrStorage          = errors.New("pool storage failure")
)

type State string

const (
	StateActive   State = "active"
	StateDisabled State = "disabled"
	StateDeleting State = "deleting"
)

type Strategy string

const (
	LeastConnections Strategy = "least_connections"
	RoundRobin       Strategy = "round_robin"
	FixedPriority    Strategy = "fixed_priority"
)

type Pool struct {
	ID        uuid.UUID         `json:"id"`
	TenantID  uuid.UUID         `json:"tenant_id"`
	Name      string            `json:"name"`
	Strategy  Strategy          `json:"strategy"`
	Selector  map[string]string `json:"selector"`
	State     State             `json:"state"`
	Version   int64             `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type Membership struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	PoolID    uuid.UUID  `json:"pool_id"`
	AccountID uuid.UUID  `json:"account_id"`
	Priority  int        `json:"priority"`
	Weight    int        `json:"weight"`
	Enabled   bool       `json:"enabled"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type Capacity struct {
	PoolID               uuid.UUID `json:"pool_id"`
	TotalMembers         int       `json:"total_members"`
	EligibleMembers      int       `json:"eligible_members"`
	TotalConcurrency     int       `json:"total_concurrency"`
	ReservedConcurrency  int       `json:"reserved_concurrency"`
	AvailableConcurrency int       `json:"available_concurrency"`
}

type CreateCommand struct {
	Name     string            `json:"name"`
	Strategy Strategy          `json:"strategy"`
	Selector map[string]string `json:"selector,omitempty"`
}
type UpdateCommand struct {
	ExpectedVersion int64              `json:"expected_version"`
	Name            *string            `json:"name,omitempty"`
	Strategy        *Strategy          `json:"strategy,omitempty"`
	Selector        *map[string]string `json:"selector,omitempty"`
	State           *State             `json:"state,omitempty"`
}
type AddMembershipCommand struct {
	AccountID uuid.UUID  `json:"account_id"`
	Priority  int        `json:"priority,omitempty"`
	Weight    int        `json:"weight,omitempty"`
	Enabled   *bool      `json:"enabled,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

var selectorKeyPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,62})$`)

func validateSelector(value map[string]string) error {
	if len(value) > 64 {
		return ErrInvalidArgument
	}
	for k, v := range value {
		if !selectorKeyPattern.MatchString(k) || len(v) > 256 {
			return ErrInvalidArgument
		}
	}
	return nil
}
func validStrategy(v Strategy) bool {
	return v == LeastConnections || v == RoundRobin || v == FixedPriority
}
func validState(v State) bool { return v == StateActive || v == StateDisabled || v == StateDeleting }
func validName(v string) bool {
	return len(strings.TrimSpace(v)) > 0 && len(strings.TrimSpace(v)) <= 200
}
