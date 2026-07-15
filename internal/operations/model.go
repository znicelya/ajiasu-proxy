package operations

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden       = errors.New("operation read is forbidden")
	ErrInvalidArgument = errors.New("invalid operation argument")
	ErrNotFound        = errors.New("operation was not found")
	ErrStorage         = errors.New("operation storage failure")
)

type Operation struct {
	ID                  uuid.UUID  `json:"id"`
	TenantID            *uuid.UUID `json:"tenant_id,omitempty"`
	OperationType       string     `json:"operation_type"`
	ResourceType        string     `json:"resource_type"`
	ResourceID          uuid.UUID  `json:"resource_id"`
	RequestedGeneration int64      `json:"requested_generation"`
	State               string     `json:"state"`
	Attempts            int        `json:"attempts"`
	ProgressCategory    string     `json:"progress_category"`
	ResultCode          string     `json:"result_code"`
	SafeMessage         string     `json:"safe_message"`
	RequestedBy         uuid.UUID  `json:"requested_by"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}
