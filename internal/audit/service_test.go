package audit_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestAppendAcceptsSafeDetailsAndPayload(t *testing.T) {
	service := audit.NewService()
	executor := &countingExecutor{}
	event, outbox := validAppendInput()
	event.Details = map[string]any{
		"reason_category": "membership_changed",
		"changes": map[string]any{
			"roles_added": []any{"auditor", "operator"},
			"attempt":     2,
		},
	}
	outbox.Payload = map[string]any{
		"tenant_id": event.TenantID.String(),
		"changes": []any{
			map[string]any{"field": "status", "value": "active"},
		},
	}

	if err := service.Append(t.Context(), executor, event, outbox); err != nil {
		t.Fatalf("Append() rejected safe details and payload: %v", err)
	}
	if executor.calls == 0 {
		t.Fatal("Append() did not invoke the database executor for safe input")
	}
}

func TestAppendRejectsSensitiveDetailKeysBeforeDatabase(t *testing.T) {
	fragments := []string{
		"db_PASSWORD_hash",
		"clientSecretValue",
		"accessTOKENdigest",
		"httpAuthorizationHeader",
		"sessionCookieValue",
		"recoveryCodes",
		"myTOTPSeed",
	}

	locations := []struct {
		name    string
		details func(string) map[string]any
	}{
		{
			name: "root",
			details: func(key string) map[string]any {
				return map[string]any{key: "canary"}
			},
		},
		{
			name: "nested_map",
			details: func(key string) map[string]any {
				return map[string]any{"outer": map[string]any{key: "canary"}}
			},
		},
		{
			name: "nested_slice",
			details: func(key string) map[string]any {
				return map[string]any{"outer": []any{"safe", map[string]any{key: "canary"}}}
			},
		},
	}

	for _, fragment := range fragments {
		for _, location := range locations {
			t.Run(fragment+"/"+location.name, func(t *testing.T) {
				event, outbox := validAppendInput()
				event.Details = location.details(fragment)
				assertAppendRejectedBeforeDatabase(t, event, outbox)
			})
		}
	}
}

func TestAppendRejectsSensitiveOutboxPayloadBeforeDatabase(t *testing.T) {
	event, outbox := validAppendInput()
	outbox.Payload = map[string]any{
		"items": []any{
			map[string]any{"serviceTokenDigest": "canary"},
		},
	}

	assertAppendRejectedBeforeDatabase(t, event, outbox)
}

func TestAppendRejectsValuesThatCannotBeEncodedAsJSONBeforeDatabase(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*audit.Event, *audit.OutboxEvent)
	}{
		{
			name: "details",
			mutate: func(event *audit.Event, _ *audit.OutboxEvent) {
				event.Details = map[string]any{"unsupported": make(chan struct{})}
			},
		},
		{
			name: "outbox_payload",
			mutate: func(_ *audit.Event, outbox *audit.OutboxEvent) {
				outbox.Payload = map[string]any{"unsupported": func() {}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, outbox := validAppendInput()
			tt.mutate(&event, &outbox)
			assertAppendRejectedBeforeDatabase(t, event, outbox)
		})
	}
}

func TestAppendValidatesRequiredFieldsUUIDsAndPayloadVersionBeforeDatabase(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*audit.Event, *audit.OutboxEvent)
	}{
		{name: "actor_type", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.ActorType = "" }},
		{name: "action", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.Action = "" }},
		{name: "resource_type", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.ResourceType = "" }},
		{name: "result", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.Result = "" }},
		{name: "source_ip", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.SourceIP = netip.Addr{} }},
		{name: "user_agent", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.UserAgent = "" }},
		{name: "request_id", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.RequestID = uuid.Nil }},
		{name: "actor_id", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { id := uuid.Nil; event.ActorID = &id }},
		{name: "tenant_id", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { id := uuid.Nil; event.TenantID = &id }},
		{name: "resource_id", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { id := uuid.Nil; event.ResourceID = &id }},
		{name: "event_type", mutate: func(_ *audit.Event, outbox *audit.OutboxEvent) { outbox.EventType = "" }},
		{name: "aggregate_type", mutate: func(_ *audit.Event, outbox *audit.OutboxEvent) { outbox.AggregateType = "" }},
		{name: "aggregate_id", mutate: func(_ *audit.Event, outbox *audit.OutboxEvent) { outbox.AggregateID = uuid.Nil }},
		{name: "payload_version_zero", mutate: func(_ *audit.Event, outbox *audit.OutboxEvent) { outbox.PayloadVersion = 0 }},
		{name: "payload_version_negative", mutate: func(_ *audit.Event, outbox *audit.OutboxEvent) { outbox.PayloadVersion = -1 }},
		{name: "details_nil", mutate: func(event *audit.Event, _ *audit.OutboxEvent) { event.Details = nil }},
		{name: "payload_nil", mutate: func(_ *audit.Event, outbox *audit.OutboxEvent) { outbox.Payload = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, outbox := validAppendInput()
			tt.mutate(&event, &outbox)
			assertAppendRejectedBeforeDatabase(t, event, outbox)
		})
	}
}

func TestLeaseOutboxValidatesRequestBeforeDatabase(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name    string
		request audit.LeaseRequest
	}{
		{name: "owner", request: audit.LeaseRequest{Limit: 1, LeaseDuration: time.Minute, Now: now}},
		{name: "limit_zero", request: audit.LeaseRequest{OwnerID: uuid.New(), LeaseDuration: time.Minute, Now: now}},
		{name: "limit_too_large", request: audit.LeaseRequest{OwnerID: uuid.New(), Limit: 101, LeaseDuration: time.Minute, Now: now}},
		{name: "duration", request: audit.LeaseRequest{OwnerID: uuid.New(), Limit: 1, Now: now}},
		{name: "now", request: audit.LeaseRequest{OwnerID: uuid.New(), Limit: 1, LeaseDuration: time.Minute}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &countingExecutor{}
			if _, err := audit.LeaseOutbox(t.Context(), executor, tt.request); err == nil {
				t.Fatal("LeaseOutbox() error = nil, want validation failure")
			}
			if executor.calls != 0 {
				t.Fatalf("LeaseOutbox() executor calls = %d, want 0", executor.calls)
			}
		})
	}
}

func TestCompleteAndReleaseOutboxValidateOwnershipBeforeDatabase(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		call func(*countingExecutor) error
	}{
		{name: "complete_event", call: func(exec *countingExecutor) error {
			_, err := audit.CompleteOutbox(t.Context(), exec, uuid.Nil, uuid.New(), now)
			return err
		}},
		{name: "complete_owner", call: func(exec *countingExecutor) error {
			_, err := audit.CompleteOutbox(t.Context(), exec, uuid.New(), uuid.Nil, now)
			return err
		}},
		{name: "complete_time", call: func(exec *countingExecutor) error {
			_, err := audit.CompleteOutbox(t.Context(), exec, uuid.New(), uuid.New(), time.Time{})
			return err
		}},
		{name: "release_event", call: func(exec *countingExecutor) error {
			_, err := audit.ReleaseOutbox(t.Context(), exec, uuid.Nil, uuid.New(), now)
			return err
		}},
		{name: "release_owner", call: func(exec *countingExecutor) error {
			_, err := audit.ReleaseOutbox(t.Context(), exec, uuid.New(), uuid.Nil, now)
			return err
		}},
		{name: "release_time", call: func(exec *countingExecutor) error {
			_, err := audit.ReleaseOutbox(t.Context(), exec, uuid.New(), uuid.New(), time.Time{})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &countingExecutor{}
			if err := tt.call(executor); err == nil {
				t.Fatal("ownership operation error = nil, want validation failure")
			}
			if executor.calls != 0 {
				t.Fatalf("ownership operation executor calls = %d, want 0", executor.calls)
			}
		})
	}
}

func assertAppendRejectedBeforeDatabase(t *testing.T, event audit.Event, outbox audit.OutboxEvent) {
	t.Helper()
	executor := &countingExecutor{}
	err := audit.NewService().Append(t.Context(), executor, event, outbox)
	if err == nil {
		t.Fatal("Append() error = nil, want validation failure")
	}
	if executor.calls != 0 {
		t.Fatalf("Append() executor calls = %d, want 0 for rejected input", executor.calls)
	}
}

func validAppendInput() (audit.Event, audit.OutboxEvent) {
	actorID := uuid.New()
	tenantID := uuid.New()
	resourceID := uuid.New()
	return audit.Event{
			ActorType:    "user",
			ActorID:      &actorID,
			TenantID:     &tenantID,
			Action:       "membership.updated",
			ResourceType: "membership",
			ResourceID:   &resourceID,
			Result:       "success",
			SourceIP:     netip.MustParseAddr("203.0.113.10"),
			UserAgent:    "audit-service-test/1.0",
			RequestID:    uuid.New(),
			Details:      map[string]any{"reason_category": "requested"},
		}, audit.OutboxEvent{
			EventType:      "tenancy.membership.updated",
			AggregateType:  "membership",
			AggregateID:    resourceID,
			PayloadVersion: 1,
			Payload:        map[string]any{"status": "active"},
			AvailableAt:    time.Now().UTC(),
		}
}

type countingExecutor struct {
	calls int
}

func (e *countingExecutor) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	e.calls++
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (e *countingExecutor) Query(context.Context, string, ...any) (pgx.Rows, error) {
	e.calls++
	return nil, nil
}

func (e *countingExecutor) QueryRow(context.Context, string, ...any) pgx.Row {
	e.calls++
	return successfulRow{}
}

type successfulRow struct{}

func (successfulRow) Scan(...any) error {
	return nil
}
