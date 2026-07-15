package gateways

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestUsageAggregatorBatchesAndDrainsDeltas(t *testing.T) {
	aggregator := NewUsageAggregator()
	tenant, endpoint := uuid.New(), uuid.New()
	start := time.Unix(100, 0).UTC()
	if err := aggregator.Add(UsageDelta{TenantID: tenant, EndpointID: endpoint, WindowStart: start, WindowSeconds: 60, ConnectionCount: 1, BytesIn: 10}); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Add(UsageDelta{TenantID: tenant, EndpointID: endpoint, WindowStart: start, WindowSeconds: 60, ConnectionCount: 2, BytesOut: 20}); err != nil {
		t.Fatal(err)
	}
	values := aggregator.Drain()
	if len(values) != 1 || values[0].ConnectionCount != 3 || values[0].BytesIn != 10 || values[0].BytesOut != 20 {
		t.Fatalf("values=%+v", values)
	}
	if len(aggregator.Drain()) != 0 {
		t.Fatal("drain did not clear")
	}
}
