package audit

import (
	"net/netip"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	ActorType    string
	ActorID      *uuid.UUID
	TenantID     *uuid.UUID
	Action       string
	ResourceType string
	ResourceID   *uuid.UUID
	Result       string
	SourceIP     netip.Addr
	UserAgent    string
	RequestID    uuid.UUID
	Details      map[string]any
}

type OutboxEvent struct {
	EventType      string
	AggregateType  string
	AggregateID    uuid.UUID
	PayloadVersion int32
	Payload        map[string]any
	AvailableAt    time.Time
}

type LeaseRequest struct {
	OwnerID       uuid.UUID
	Limit         int32
	LeaseDuration time.Duration
	Now           time.Time
}

type OutboxRecord struct {
	ID             uuid.UUID
	TenantID       *uuid.UUID
	EventType      string
	AggregateType  string
	AggregateID    uuid.UUID
	PayloadVersion int32
	Payload        map[string]any
	CreatedAt      time.Time
	AvailableAt    time.Time
	LeaseOwner     *uuid.UUID
	LeaseDeadline  *time.Time
	Attempts       int32
	ProcessedAt    *time.Time
}

type auditRow struct {
	ID           uuid.UUID
	ActorType    string
	ActorID      *uuid.UUID
	TenantID     *uuid.UUID
	Action       string
	ResourceType string
	ResourceID   *uuid.UUID
	Result       string
	SourceIP     netip.Addr
	UserAgent    string
	RequestID    uuid.UUID
	DetailsJSON  []byte
	CreatedAt    time.Time
}

type outboxRow struct {
	ID             uuid.UUID
	TenantID       *uuid.UUID
	EventType      string
	AggregateType  string
	AggregateID    uuid.UUID
	PayloadVersion int32
	PayloadJSON    []byte
	CreatedAt      time.Time
	AvailableAt    time.Time
}
