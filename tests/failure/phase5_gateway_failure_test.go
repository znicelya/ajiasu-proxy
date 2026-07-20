package failure

import (
	"github.com/znicelya/ajiasu-proxy/internal/gateways"
	"testing"
	"time"
)

func TestPhase5GatewayRejectsReorderedAndExpiredRoutes(t *testing.T) {
	var applier = gateways.NewSnapshotApplier()
	if err := applier.ApplySnapshot(gateways.Snapshot{Version: 2, GeneratedAt: time.Now(), Routes: nil}); err != nil {
		t.Fatal(err)
	}
	if err := applier.ApplySnapshot(gateways.Snapshot{Version: 1}); err != gateways.ErrStaleVersion {
		t.Fatalf("err=%v", err)
	}
}
