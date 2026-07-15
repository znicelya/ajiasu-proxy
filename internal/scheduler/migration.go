package scheduler

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrMigrationInvalid       = errors.New("migration request is invalid")
	ErrFixedEndpointMigration = errors.New("fixed endpoint cannot migrate automatically")
	ErrMigrationExhausted     = errors.New("migration retry budget exhausted")
	ErrReplacementNotReady    = errors.New("replacement runner is not ready")
)

type FailureClass string

const (
	FailureRunner  FailureClass = "runner"
	FailureAccount FailureClass = "account"
	FailureNode    FailureClass = "node"
)

type MigrationType string

const (
	MigrationRunnerRebuild      MigrationType = "runner_rebuild"
	MigrationAccountReplacement MigrationType = "account_replacement"
	MigrationNode               MigrationType = "node_migration"
)

type MigrationPolicy struct {
	MaxRunnerRebuilds      int
	MaxAccountReplacements int
	MaxNodeMigrations      int
	CooldownBase           time.Duration
	CooldownMaximum        time.Duration
}

func DefaultMigrationPolicy() MigrationPolicy {
	return MigrationPolicy{MaxRunnerRebuilds: 3, MaxAccountReplacements: 3, MaxNodeMigrations: 3, CooldownBase: time.Second, CooldownMaximum: time.Minute}
}
func (policy MigrationPolicy) Validate() error {
	if policy.MaxRunnerRebuilds < 1 || policy.MaxRunnerRebuilds > 100 || policy.MaxAccountReplacements < 1 || policy.MaxAccountReplacements > 100 || policy.MaxNodeMigrations < 1 || policy.MaxNodeMigrations > 100 || policy.CooldownBase <= 0 || policy.CooldownMaximum < policy.CooldownBase {
		return ErrMigrationInvalid
	}
	return nil
}

type Failure struct {
	Class      FailureClass
	ReasonCode string
	ObservedAt time.Time
}
type MigrationPlan struct {
	Type              MigrationType
	AssignmentID      uuid.UUID
	EndpointID        uuid.UUID
	SourceAccountID   *uuid.UUID
	SourceNodeID      *uuid.UUID
	DesiredGeneration int64
	FencingToken      uint64
	Attempt           int
	CooldownUntil     time.Time
	ReasonCode        string
}
type ReplacementObservation struct {
	AssignmentID   uuid.UUID
	RunnerID       uuid.UUID
	Generation     int64
	FencingToken   uint64
	Running        bool
	RoutePublished bool
}

func PlanMigration(assignment Assignment, bindingMode string, failure Failure, incomingFence uint64, attempt int, policy MigrationPolicy) (MigrationPlan, error) {
	if policy.Validate() != nil || assignment.AssignmentID == uuid.Nil || assignment.EndpointID == uuid.Nil || failure.ObservedAt.IsZero() || failure.ReasonCode == "" || attempt < 1 || incomingFence == 0 {
		return MigrationPlan{}, ErrMigrationInvalid
	}
	if bindingMode != "pool" {
		return MigrationPlan{}, ErrFixedEndpointMigration
	}
	if err := CheckFencingToken(uint64(assignment.FencingToken), incomingFence); err != nil {
		return MigrationPlan{}, err
	}
	var migrationType MigrationType
	var maximum int
	switch failure.Class {
	case FailureRunner:
		migrationType, maximum = MigrationRunnerRebuild, policy.MaxRunnerRebuilds
	case FailureAccount:
		migrationType, maximum = MigrationAccountReplacement, policy.MaxAccountReplacements
	case FailureNode:
		migrationType, maximum = MigrationNode, policy.MaxNodeMigrations
	default:
		return MigrationPlan{}, ErrMigrationInvalid
	}
	if attempt > maximum {
		return MigrationPlan{}, ErrMigrationExhausted
	}
	return MigrationPlan{Type: migrationType, AssignmentID: assignment.AssignmentID, EndpointID: assignment.EndpointID, SourceAccountID: assignment.AccountID, SourceNodeID: assignment.NodeID, DesiredGeneration: assignment.DesiredGeneration + 1, FencingToken: incomingFence, Attempt: attempt, CooldownUntil: failure.ObservedAt.Add(migrationBackoff(policy.CooldownBase, policy.CooldownMaximum, attempt)), ReasonCode: failure.ReasonCode}, nil
}

func ValidateReplacement(plan MigrationPlan, observation ReplacementObservation) error {
	if plan.AssignmentID == uuid.Nil || observation.AssignmentID != plan.AssignmentID || observation.RunnerID == uuid.Nil || observation.Generation != plan.DesiredGeneration || observation.FencingToken != plan.FencingToken || !observation.Running || !observation.RoutePublished {
		return ErrReplacementNotReady
	}
	return nil
}
func migrationBackoff(base, maximum time.Duration, attempt int) time.Duration {
	value := base
	for current := 1; current < attempt && value < maximum; current++ {
		if value > maximum/2 {
			return maximum
		}
		value *= 2
	}
	if value > maximum {
		return maximum
	}
	return value
}
