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
	now := time.Unix(100, 0).UTC()
	snapshot, err := BuildSnapshot(2, now, private, []SnapshotInput{{GatewayID: gatewayID, TenantID: tenantID, EndpointID: endpointID, RunnerID: runnerID, Generation: 7, PolicyHash: "abc", Protocols: []string{"socks5", "http"}, GrantExpiry: now.Add(time.Minute)}})
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
	if err := applier.ApplyDelta(Delta{Version: 2, Route: snapshot.Routes[0]}); err != ErrStaleVersion {
		t.Fatalf("err=%v", err)
	}
	if err := applier.ApplyDelta(Delta{Version: 3, Route: snapshot.Routes[0], Revoked: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := applier.Route(tenantID, endpointID); ok {
		t.Fatal("revoked route remains")
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
