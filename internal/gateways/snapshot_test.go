package gateways

import (
	"crypto/ed25519"
	"github.com/google/uuid"
	"testing"
	"time"
)

type fakeStream struct {
	closed   bool
	messages []any
}

func (f *fakeStream) Send(message any) error { f.messages = append(f.messages, message); return nil }
func (f *fakeStream) Close() error           { f.closed = true; return nil }

func TestSnapshotGrantAndMonotonicApplication(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	gatewayID, tenantID, endpointID, runnerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	assignmentID, accountID, nodeID := uuid.New(), uuid.New(), uuid.New()
	now := time.Unix(100, 0).UTC()
	snapshot, err := BuildSnapshot(2, now, private, []SnapshotInput{{GatewayID: gatewayID, TenantID: tenantID, EndpointID: endpointID, RunnerID: runnerID, AssignmentID: assignmentID, AccountID: accountID, NodeID: nodeID, Generation: 7, AssignmentGeneration: 7, AssignmentState: "assigned", PolicyHash: "abc", Protocols: []string{"socks5", "http"}, GrantExpiry: now.Add(time.Minute), ValidUntil: now.Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Routes[0].Grant.Verify(public, gatewayID, now); err != nil {
		t.Fatal(err)
	}
	applier := NewSnapshotApplier()
	if err := applier.ApplySnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := applier.ApplyDelta(Delta{Version: 2, Route: snapshot.Routes[0]}); err != nil {
		t.Fatalf("duplicate delta err=%v", err)
	}
	if err := applier.ApplyDelta(Delta{Version: 3, Route: snapshot.Routes[0], Revoked: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := applier.Route(tenantID, endpointID); ok {
		t.Fatal("revoked route remains")
	}
}

func TestRouteAssignmentOrderingDrainingAndSnapshotRecovery(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(nil)
	now := time.Unix(200, 0).UTC()
	input := SnapshotInput{GatewayID: uuid.New(), TenantID: uuid.New(), EndpointID: uuid.New(), RunnerID: uuid.New(), AssignmentID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New(), Generation: 4, AssignmentGeneration: 4, AssignmentState: "assigned", PolicyHash: "hash", Protocols: []string{"connect"}, GrantExpiry: now.Add(time.Minute), ValidUntil: now.Add(time.Minute)}
	snapshot, err := BuildSnapshot(5, now, private, []SnapshotInput{input})
	if err != nil {
		t.Fatal(err)
	}
	applier := NewSnapshotApplier()
	if err := applier.ApplySnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := applier.ApplyDelta(Delta{Version: 5, Route: snapshot.Routes[0]}); err != nil {
		t.Fatalf("duplicate delta: %v", err)
	}
	if err := applier.ApplyDelta(Delta{Version: 7, Route: snapshot.Routes[0]}); err != ErrSnapshotRequired {
		t.Fatalf("gap err=%v", err)
	}
	draining := snapshot.Routes[0]
	draining.AssignmentState = "draining"
	if err := applier.ApplyDelta(Delta{Version: 6, Route: draining}); err != nil {
		t.Fatal(err)
	}
	if _, err := applier.Select(input.TenantID, input.EndpointID, now); err != ErrRouteUnavailable {
		t.Fatalf("draining route err=%v", err)
	}
	stale := draining
	stale.AssignmentGeneration--
	stale.Grant.Generation--
	if err := applier.ApplyDelta(Delta{Version: 7, Route: stale}); err != ErrStaleAssignment {
		t.Fatalf("stale assignment err=%v", err)
	}
}

func TestStreamReplacementClosesPreviousStream(t *testing.T) {
	registry := NewStreamRegistry()
	first, second := &fakeStream{}, &fakeStream{}
	if _, err := registry.Replace(uuid.New(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Replace(uuid.New(), second); err != nil {
		t.Fatal(err)
	}
	if first.closed {
		t.Fatal("different gateway stream was closed")
	}
	gateway := uuid.New()
	if _, err := registry.Replace(gateway, first); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Replace(gateway, second); err != nil {
		t.Fatal(err)
	}
	if !first.closed {
		t.Fatal("old stream not closed")
	}
}
