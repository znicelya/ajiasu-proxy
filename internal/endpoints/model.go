package endpoints

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden              = errors.New("endpoint operation is forbidden")
	ErrInvalidArgument        = errors.New("invalid endpoint argument")
	ErrNotFound               = errors.New("endpoint was not found")
	ErrAlreadyExists          = errors.New("endpoint already exists")
	ErrVersionConflict        = errors.New("endpoint version conflict")
	ErrNodeUnavailable        = errors.New("endpoint node is unavailable")
	ErrNodeCapacity           = errors.New("endpoint node capacity is exhausted")
	ErrAccountCapacity        = errors.New("endpoint account capacity is exhausted")
	ErrStorage                = errors.New("endpoint storage failure")
	ErrPoolAssignmentRequired = errors.New("pool endpoint requires scheduler assignment")
)

type DesiredRunnerState string

const (
	DesiredRunning DesiredRunnerState = "running"
	DesiredStopped DesiredRunnerState = "stopped"
)

type LifecycleState string

const (
	LifecycleActive   LifecycleState = "active"
	LifecycleDisabled LifecycleState = "disabled"
	LifecycleDeleting LifecycleState = "deleting"
)

type ObservedState string

const (
	ObservedPending  ObservedState = "pending"
	ObservedStarting ObservedState = "starting"
	ObservedRunning  ObservedState = "running"
	ObservedStopping ObservedState = "stopping"
	ObservedStopped  ObservedState = "stopped"
	ObservedFailed   ObservedState = "failed"
	ObservedOrphaned ObservedState = "orphaned"
)

type Endpoint struct {
	ID                 uuid.UUID          `json:"id"`
	TenantID           uuid.UUID          `json:"tenant_id"`
	Name               string             `json:"name"`
	BindingMode        string             `json:"binding_mode"`
	AccountID          uuid.UUID          `json:"account_id,omitempty"`
	NodeID             uuid.UUID          `json:"node_id,omitempty"`
	PoolID             *uuid.UUID         `json:"pool_id,omitempty"`
	DesiredRunnerState DesiredRunnerState `json:"desired_runner_state"`
	LifecycleState     LifecycleState     `json:"lifecycle_state"`
	DesiredGeneration  int64              `json:"desired_generation"`
	Version            int64              `json:"version"`
	Status             Status             `json:"status"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
}
type Status struct {
	ObservedGeneration     int64         `json:"observed_generation"`
	ObservedState          ObservedState `json:"observed_state"`
	RunnerID               *uuid.UUID    `json:"runner_id,omitempty"`
	ReasonCode             string        `json:"reason_code"`
	LastAgentObservationAt *time.Time    `json:"last_agent_observation_at,omitempty"`
	LastTransitionAt       time.Time     `json:"last_transition_at"`
}
type Operation struct {
	ID                  uuid.UUID `json:"id"`
	TenantID            uuid.UUID `json:"tenant_id"`
	OperationType       string    `json:"operation_type"`
	ResourceType        string    `json:"resource_type"`
	ResourceID          uuid.UUID `json:"resource_id"`
	RequestedGeneration int64     `json:"requested_generation"`
	State               string    `json:"state"`
	ProgressCategory    string    `json:"progress_category"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}
type CreateCommand struct {
	Name               string
	BindingMode        string
	AccountID          uuid.UUID
	NodeID             uuid.UUID
	PoolID             uuid.UUID
	DesiredRunnerState DesiredRunnerState
}

func (c CreateCommand) Validate() error {
	if len(strings.TrimSpace(c.Name)) == 0 || len(strings.TrimSpace(c.Name)) > 200 {
		return ErrInvalidArgument
	}
	if c.BindingMode == "" {
		c.BindingMode = "fixed"
	}
	if (c.BindingMode == "fixed" && (c.AccountID == uuid.Nil || c.NodeID == uuid.Nil || c.PoolID != uuid.Nil)) || (c.BindingMode == "pool" && (c.PoolID == uuid.Nil || c.AccountID != uuid.Nil || c.NodeID != uuid.Nil)) {
		return ErrInvalidArgument
	}
	if c.DesiredRunnerState == "" {
		c.DesiredRunnerState = DesiredRunning
	}
	if c.DesiredRunnerState != DesiredRunning && c.DesiredRunnerState != DesiredStopped {
		return ErrInvalidArgument
	}
	return nil
}
