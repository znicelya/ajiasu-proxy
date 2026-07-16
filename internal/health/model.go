package health

import (
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidObservation = errors.New("health observation is invalid")
	ErrStaleObservation   = errors.New("health observation is stale")
)

type Dimension string
type Result string
type Status string

const (
	DimensionProcess  Dimension = "process"
	DimensionTunnel   Dimension = "tunnel"
	DimensionEgress   Dimension = "egress"
	DimensionAccount  Dimension = "account"
	ResultSuccess     Result    = "success"
	ResultFailure     Result    = "failure"
	StatusUnknown     Status    = "unknown"
	StatusHealthy     Status    = "healthy"
	StatusDegraded    Status    = "degraded"
	StatusUnhealthy   Status    = "unhealthy"
	StatusQuarantined Status    = "quarantined"
)

type Observation struct {
	TenantID     uuid.UUID
	ResourceType string
	ResourceID   uuid.UUID
	Dimension    Dimension
	Result       Result
	Generation   uint64
	Sequence     uint64
	ReasonCode   string
	ObservedAt   time.Time
}

type State struct {
	TenantID             uuid.UUID
	ResourceType         string
	ResourceID           uuid.UUID
	Dimension            Dimension
	Status               Status
	Generation           uint64
	LastSequence         uint64
	ConsecutiveSuccesses int
	ConsecutiveFailures  int
	QuarantineCount      int
	ReasonCode           string
	CooldownUntil        *time.Time
	LastObservedAt       time.Time
	LastTransitionAt     time.Time
}

type Threshold struct {
	DegradedFailures  int
	UnhealthyFailures int
}
type Config struct {
	Process                   Threshold
	Tunnel                    Threshold
	Egress                    Threshold
	AccountQuarantineFailures int
	RecoverySuccesses         int
	CooldownBase              time.Duration
	CooldownMaximum           time.Duration
}

func DefaultConfig() Config {
	return Config{Process: Threshold{2, 3}, Tunnel: Threshold{3, 5}, Egress: Threshold{3, 5}, AccountQuarantineFailures: 3, RecoverySuccesses: 3, CooldownBase: time.Minute, CooldownMaximum: time.Hour}
}

func (config Config) Validate() error {
	for _, threshold := range []Threshold{config.Process, config.Tunnel, config.Egress} {
		if threshold.DegradedFailures < 1 || threshold.UnhealthyFailures < threshold.DegradedFailures || threshold.UnhealthyFailures > 100 {
			return ErrInvalidObservation
		}
	}
	if config.AccountQuarantineFailures < 1 || config.AccountQuarantineFailures > 100 || config.RecoverySuccesses < 1 || config.RecoverySuccesses > 100 || config.CooldownBase <= 0 || config.CooldownMaximum < config.CooldownBase || config.CooldownMaximum > 24*time.Hour {
		return ErrInvalidObservation
	}
	return nil
}

var reasonPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]{0,127})$`)

func (observation Observation) Validate() error {
	if observation.TenantID == uuid.Nil || observation.ResourceID == uuid.Nil || observation.Generation == 0 || observation.Sequence == 0 || observation.ObservedAt.IsZero() || !reasonPattern.MatchString(observation.ReasonCode) {
		return ErrInvalidObservation
	}
	if observation.ResourceType != "account" && observation.ResourceType != "endpoint" && observation.ResourceType != "runner" && observation.ResourceType != "assignment" {
		return ErrInvalidObservation
	}
	if observation.Dimension != DimensionProcess && observation.Dimension != DimensionTunnel && observation.Dimension != DimensionEgress && observation.Dimension != DimensionAccount {
		return ErrInvalidObservation
	}
	if observation.Result != ResultSuccess && observation.Result != ResultFailure {
		return ErrInvalidObservation
	}
	return nil
}

type Transition struct {
	From       Status
	To         Status
	Dimension  Dimension
	ReasonCode string
	Generation uint64
	Sequence   uint64
	OccurredAt time.Time
}
