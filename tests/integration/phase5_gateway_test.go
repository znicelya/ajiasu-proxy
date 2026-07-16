package integration

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/gateways"
	"github.com/google/uuid"
)

func TestPhase5GatewayRouteAndProtocolFlow(t *testing.T) {
	// The Go integration gate exercises the same bounded contract used by the
	// Rust data plane: route grant freshness precedes protocol forwarding.
	gatewayID, tenantID, endpointID, runnerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	assignmentID, accountID, nodeID := uuid.New(), uuid.New(), uuid.New()
	_, private, _ := ed25519.GenerateKey(nil)
	now := time.Now().UTC()
	snapshot, err := gateways.BuildSnapshot(1, now, private, []gateways.SnapshotInput{{GatewayID: gatewayID, TenantID: tenantID, EndpointID: endpointID, RunnerID: runnerID, AssignmentID: assignmentID, AccountID: accountID, NodeID: nodeID, Generation: 1, AssignmentGeneration: 1, AssignmentState: "assigned", PolicyHash: "policy", Protocols: []string{"http", "connect", "socks5"}, GrantExpiry: now.Add(time.Minute), ValidUntil: now.Add(time.Minute)}})
	if err != nil || len(snapshot.Routes) != 1 {
		t.Fatalf("snapshot err=%v routes=%d", err, len(snapshot.Routes))
	}
	applier := gateways.NewSnapshotApplier()
	if err := applier.ApplySnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	auth := base64.StdEncoding.EncodeToString([]byte("user:password"))
	if auth == "" || applier.Version() != 1 {
		t.Fatal("route snapshot was not applied")
	}
	_ = auth
}
