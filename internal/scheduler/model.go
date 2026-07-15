package scheduler

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNoEligibleCandidate = errors.New("no_eligible_candidate")
	ErrCapacityExhausted   = errors.New("capacity_exhausted")
	ErrSchedulerInvalid    = errors.New("scheduler request is invalid")
)

type Strategy string

const (
	StrategyLeastConnections Strategy = "least_connections"
	StrategyRoundRobin       Strategy = "round_robin"
	StrategyFixedPriority    Strategy = "fixed_priority"
)

type HealthState string

const (
	HealthUnknown     HealthState = "unknown"
	HealthHealthy     HealthState = "healthy"
	HealthDegraded    HealthState = "degraded"
	HealthUnhealthy   HealthState = "unhealthy"
	HealthQuarantined HealthState = "quarantined"
)

type Candidate struct {
	TenantID             uuid.UUID
	PoolID               uuid.UUID
	MembershipID         uuid.UUID
	AccountID            uuid.UUID
	NodeID               uuid.UUID
	AccountLabels        map[string]string
	AccountState         string
	AccountHealth        HealthState
	NodeMaintenance      string
	NodeConnectivity     string
	NodeArchitecture     string
	NodeCapabilities     map[string]bool
	MembershipEnabled    bool
	MembershipExpiresAt  *time.Time
	Priority             int
	Weight               int
	MaxConcurrency       int
	ReservedConcurrency  int
	ActiveConnections    int64
	NodeMaxRunners       int
	NodeReservedHeadroom int
	NodeActiveRunners    int
	CooldownUntil        *time.Time
}

type SelectionRequest struct {
	TenantID             uuid.UUID
	PoolID               uuid.UUID
	Strategy             Strategy
	Selector             map[string]string
	RequiredArchitecture string
	RequiredCapabilities []string
	RoundRobinCursor     uint64
	Now                  time.Time
}

type Selection struct {
	Candidate  Candidate
	NextCursor uint64
}

func (request SelectionRequest) Validate() error {
	if request.TenantID == uuid.Nil || request.PoolID == uuid.Nil || request.Now.IsZero() {
		return ErrSchedulerInvalid
	}
	if request.Strategy != StrategyLeastConnections && request.Strategy != StrategyRoundRobin && request.Strategy != StrategyFixedPriority {
		return ErrSchedulerInvalid
	}
	if len(request.Selector) > 64 || len(request.RequiredCapabilities) > 64 {
		return ErrSchedulerInvalid
	}
	return nil
}
