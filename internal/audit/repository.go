package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit/dbgen"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/google/uuid"
)

const (
	maxLeaseBatch    = 100
	maxLeaseDuration = 24 * time.Hour
)

func appendRows(ctx context.Context, executor database.Executor, auditEvent auditRow, outboxEvent outboxRow) error {
	queries := dbgen.New(executor)
	if err := queries.InsertAuditAndOutbox(ctx, dbgen.InsertAuditAndOutboxParams{
		OutboxID:        outboxEvent.ID,
		OutboxTenantID:  outboxEvent.TenantID,
		EventType:       outboxEvent.EventType,
		AggregateType:   outboxEvent.AggregateType,
		AggregateID:     outboxEvent.AggregateID,
		PayloadVersion:  outboxEvent.PayloadVersion,
		Payload:         outboxEvent.PayloadJSON,
		OutboxCreatedAt: outboxEvent.CreatedAt.UTC(),
		AvailableAt:     outboxEvent.AvailableAt.UTC(),
		AuditID:         auditEvent.ID,
		AuditTenantID:   auditEvent.TenantID,
		ActorType:       auditEvent.ActorType,
		ActorID:         auditEvent.ActorID,
		Action:          auditEvent.Action,
		ResourceType:    auditEvent.ResourceType,
		ResourceID:      auditEvent.ResourceID,
		Result:          auditEvent.Result,
		SourceIp:        auditEvent.SourceIP,
		UserAgent:       auditEvent.UserAgent,
		RequestID:       auditEvent.RequestID,
		Details:         auditEvent.DetailsJSON,
		AuditCreatedAt:  auditEvent.CreatedAt.UTC(),
	}); err != nil {
		return fmt.Errorf("insert audit and outbox events: %w", err)
	}
	return nil
}

func LeaseOutbox(ctx context.Context, executor database.Executor, request LeaseRequest) ([]OutboxRecord, error) {
	if executor == nil {
		return nil, errors.New("outbox executor is required")
	}
	if request.OwnerID == uuid.Nil {
		return nil, errors.New("lease owner ID is required")
	}
	if request.Limit <= 0 || request.Limit > maxLeaseBatch {
		return nil, fmt.Errorf("lease batch limit must be between 1 and %d", maxLeaseBatch)
	}
	if request.LeaseDuration <= 0 || request.LeaseDuration > maxLeaseDuration {
		return nil, errors.New("lease duration is invalid")
	}
	if request.Now.IsZero() {
		return nil, errors.New("lease time is required")
	}

	ownerID := request.OwnerID
	leaseDeadline := request.Now.Add(request.LeaseDuration).UTC()
	rows, err := dbgen.New(executor).LeaseOutboxEvents(ctx, dbgen.LeaseOutboxEventsParams{
		OwnerID:       &ownerID,
		LeaseDeadline: &leaseDeadline,
		LeaseNow:      request.Now.UTC(),
		BatchLimit:    request.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("lease outbox events: %w", err)
	}
	records := make([]OutboxRecord, 0, len(rows))
	for _, row := range rows {
		payload := make(map[string]any)
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode leased outbox payload: %w", err)
		}
		records = append(records, OutboxRecord{
			ID:             row.ID,
			TenantID:       row.TenantID,
			EventType:      row.EventType,
			AggregateType:  row.AggregateType,
			AggregateID:    row.AggregateID,
			PayloadVersion: row.PayloadVersion,
			Payload:        payload,
			CreatedAt:      row.CreatedAt.UTC(),
			AvailableAt:    row.AvailableAt.UTC(),
			LeaseOwner:     row.LeaseOwner,
			LeaseDeadline:  utcTimePointer(row.LeaseDeadline),
			Attempts:       row.Attempts,
			ProcessedAt:    utcTimePointer(row.ProcessedAt),
		})
	}
	return records, nil
}

func CompleteOutbox(ctx context.Context, executor database.Executor, eventID, ownerID uuid.UUID, processedAt time.Time) (bool, error) {
	if err := validateOwnershipOperation(executor, eventID, ownerID, processedAt); err != nil {
		return false, err
	}
	owner := ownerID
	processed := processedAt.UTC()
	rows, err := dbgen.New(executor).CompleteOutboxEvent(ctx, dbgen.CompleteOutboxEventParams{
		ProcessedAt: &processed,
		ID:          eventID,
		OwnerID:     &owner,
	})
	if err != nil {
		return false, fmt.Errorf("complete outbox event: %w", err)
	}
	return rows == 1, nil
}

func ReleaseOutbox(ctx context.Context, executor database.Executor, eventID, ownerID uuid.UUID, availableAt time.Time) (bool, error) {
	if err := validateOwnershipOperation(executor, eventID, ownerID, availableAt); err != nil {
		return false, err
	}
	owner := ownerID
	rows, err := dbgen.New(executor).ReleaseOutboxEvent(ctx, dbgen.ReleaseOutboxEventParams{
		AvailableAt: availableAt.UTC(),
		ID:          eventID,
		OwnerID:     &owner,
	})
	if err != nil {
		return false, fmt.Errorf("release outbox event: %w", err)
	}
	return rows == 1, nil
}

func validateOwnershipOperation(executor database.Executor, eventID, ownerID uuid.UUID, timestamp time.Time) error {
	if executor == nil {
		return errors.New("outbox executor is required")
	}
	if eventID == uuid.Nil {
		return errors.New("outbox event ID is required")
	}
	if ownerID == uuid.Nil {
		return errors.New("lease owner ID is required")
	}
	if timestamp.IsZero() {
		return errors.New("outbox timestamp is required")
	}
	return nil
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}
