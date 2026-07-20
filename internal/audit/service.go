package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/google/uuid"
)

const (
	maxActorTypeLength    = 64
	maxActionLength       = 128
	maxResourceTypeLength = 64
	maxResultLength       = 64
	maxUserAgentLength    = 1024
)

var sensitiveKeyFragments = [...]string{
	"password",
	"secret",
	"token",
	"authorization",
	"cookie",
	"recovery",
	"totp",
}

type Service interface {
	Append(context.Context, database.Executor, Event, OutboxEvent) error
}

type service struct {
	now   func() time.Time
	newID func() (uuid.UUID, error)
}

func NewService() Service {
	return &service{
		now:   func() time.Time { return time.Now().UTC() },
		newID: uuid.NewV7,
	}
}

func (s *service) Append(ctx context.Context, executor database.Executor, event Event, outbox OutboxEvent) error {
	if executor == nil {
		return errors.New("audit executor is required")
	}
	if err := validateEvent(event); err != nil {
		return err
	}
	if err := validateOutboxEvent(outbox); err != nil {
		return err
	}
	detailsJSON, err := marshalSafeObject("audit details", event.Details)
	if err != nil {
		return err
	}
	payloadJSON, err := marshalSafeObject("outbox payload", outbox.Payload)
	if err != nil {
		return err
	}

	auditID, err := s.newID()
	if err != nil {
		return fmt.Errorf("generate audit event ID: %w", err)
	}
	outboxID, err := s.newID()
	if err != nil {
		return fmt.Errorf("generate outbox event ID: %w", err)
	}
	createdAt := s.now().UTC()
	return appendRows(ctx, executor, auditRow{
		ID:           auditID,
		ActorType:    strings.TrimSpace(event.ActorType),
		ActorID:      event.ActorID,
		TenantID:     event.TenantID,
		Action:       strings.TrimSpace(event.Action),
		ResourceType: strings.TrimSpace(event.ResourceType),
		ResourceID:   event.ResourceID,
		Result:       strings.TrimSpace(event.Result),
		SourceIP:     event.SourceIP.Unmap(),
		UserAgent:    strings.TrimSpace(event.UserAgent),
		RequestID:    event.RequestID,
		DetailsJSON:  detailsJSON,
		CreatedAt:    createdAt,
	}, outboxRow{
		ID:             outboxID,
		TenantID:       event.TenantID,
		EventType:      strings.TrimSpace(outbox.EventType),
		AggregateType:  strings.TrimSpace(outbox.AggregateType),
		AggregateID:    outbox.AggregateID,
		PayloadVersion: outbox.PayloadVersion,
		PayloadJSON:    payloadJSON,
		CreatedAt:      createdAt,
		AvailableAt:    outbox.AvailableAt.UTC(),
	})
}

func validateEvent(event Event) error {
	if err := validateRequiredText("actor type", event.ActorType, maxActorTypeLength); err != nil {
		return err
	}
	if err := validateRequiredText("action", event.Action, maxActionLength); err != nil {
		return err
	}
	if err := validateRequiredText("resource type", event.ResourceType, maxResourceTypeLength); err != nil {
		return err
	}
	if err := validateRequiredText("result", event.Result, maxResultLength); err != nil {
		return err
	}
	if err := validateRequiredText("user agent", event.UserAgent, maxUserAgentLength); err != nil {
		return err
	}
	if !event.SourceIP.IsValid() {
		return errors.New("source IP is required")
	}
	if event.RequestID == uuid.Nil {
		return errors.New("request ID is required")
	}
	if err := validateOptionalUUID("actor ID", event.ActorID); err != nil {
		return err
	}
	if err := validateOptionalUUID("tenant ID", event.TenantID); err != nil {
		return err
	}
	if err := validateOptionalUUID("resource ID", event.ResourceID); err != nil {
		return err
	}
	if event.Details == nil {
		return errors.New("audit details are required")
	}
	return nil
}

func validateOutboxEvent(event OutboxEvent) error {
	if err := validateRequiredText("event type", event.EventType, maxActionLength); err != nil {
		return err
	}
	if err := validateRequiredText("aggregate type", event.AggregateType, maxResourceTypeLength); err != nil {
		return err
	}
	if event.AggregateID == uuid.Nil {
		return errors.New("aggregate ID is required")
	}
	if event.PayloadVersion <= 0 {
		return errors.New("payload version must be positive")
	}
	if event.Payload == nil {
		return errors.New("outbox payload is required")
	}
	if event.AvailableAt.IsZero() {
		return errors.New("outbox availability time is required")
	}
	return nil
}

func validateRequiredText(name, value string, maximum int) error {
	length := len(strings.TrimSpace(value))
	if length == 0 {
		return fmt.Errorf("%s is required", name)
	}
	if length > maximum {
		return fmt.Errorf("%s is too long", name)
	}
	return nil
}

func validateOptionalUUID(name string, value *uuid.UUID) error {
	if value != nil && *value == uuid.Nil {
		return fmt.Errorf("%s must not be zero", name)
	}
	return nil
}

func marshalSafeObject(name string, value map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", name, err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, fmt.Errorf("normalize %s: %w", name, err)
	}
	if err := rejectSensitiveKeys(normalized); err != nil {
		return nil, fmt.Errorf("validate %s: %w", name, err)
	}
	return encoded, nil
}

func rejectSensitiveKeys(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			lowerKey := strings.ToLower(key)
			for _, fragment := range sensitiveKeyFragments {
				if strings.Contains(lowerKey, fragment) {
					return fmt.Errorf("field key contains forbidden fragment %q", fragment)
				}
			}
			if err := rejectSensitiveKeys(nested); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := rejectSensitiveKeys(nested); err != nil {
				return err
			}
		}
	}
	return nil
}
