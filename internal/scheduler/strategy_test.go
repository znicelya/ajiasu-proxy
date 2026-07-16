package scheduler

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func candidateFixture(tenantID, poolID uuid.UUID, ordinal uint64) Candidate {
	return Candidate{TenantID: tenantID, PoolID: poolID, MembershipID: uuid.MustParse("00000000-0000-0000-0000-" + formatOrdinal(ordinal)), AccountID: uuid.MustParse("10000000-0000-0000-0000-" + formatOrdinal(ordinal)), NodeID: uuid.MustParse("20000000-0000-0000-0000-" + formatOrdinal(ordinal)), AccountLabels: map[string]string{"region": "ap"}, AccountState: "active", AccountHealth: HealthHealthy, NodeMaintenance: "active", NodeConnectivity: "online", NodeArchitecture: "amd64", NodeCapabilities: map[string]bool{"relay": true}, MembershipEnabled: true, Priority: 100, Weight: 1, MaxConcurrency: 10, NodeMaxRunners: 10, NodeReservedHeadroom: 1}
}

func formatOrdinal(value uint64) string { return fmt.Sprintf("%012d", value) }

func TestSchedulerStrategiesAreDeterministic(t *testing.T) {
	tenantID, poolID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	one, two, three := candidateFixture(tenantID, poolID, 1), candidateFixture(tenantID, poolID, 2), candidateFixture(tenantID, poolID, 3)
	one.ActiveConnections, one.ReservedConcurrency = 5, 1
	two.ActiveConnections, two.ReservedConcurrency = 1, 8
	three.ActiveConnections, three.ReservedConcurrency = 1, 2
	request := SelectionRequest{TenantID: tenantID, PoolID: poolID, Strategy: StrategyLeastConnections, Selector: map[string]string{"region": "ap"}, RequiredArchitecture: "amd64", RequiredCapabilities: []string{"relay"}, Now: now}
	selection, err := SelectCandidate(request, []Candidate{two, one, three})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Candidate.AccountID != three.AccountID {
		t.Fatalf("least selected %s", selection.Candidate.AccountID)
	}
	two.Priority, three.Priority = 10, 20
	two.AccountHealth = HealthDegraded
	request.Strategy = StrategyFixedPriority
	selection, err = SelectCandidate(request, []Candidate{three, two})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Candidate.AccountID != two.AccountID {
		t.Fatalf("priority selected %s", selection.Candidate.AccountID)
	}
	request.Strategy, request.RoundRobinCursor = StrategyRoundRobin, 1
	selection, err = SelectCandidate(request, []Candidate{three, one, two})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Candidate.MembershipID != two.MembershipID || selection.NextCursor != 2 {
		t.Fatalf("round robin=%+v", selection)
	}
}

func TestSchedulerFiltersHealthExpirySelectorCooldownAndCapacity(t *testing.T) {
	tenantID, poolID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	request := SelectionRequest{TenantID: tenantID, PoolID: poolID, Strategy: StrategyLeastConnections, Selector: map[string]string{"region": "ap"}, Now: now}
	base := candidateFixture(tenantID, poolID, 1)
	tests := []struct {
		name   string
		mutate func(*Candidate)
	}{
		{"quarantined", func(c *Candidate) { c.AccountHealth = HealthQuarantined }},
		{"expired", func(c *Candidate) { value := now; c.MembershipExpiresAt = &value }},
		{"selector", func(c *Candidate) { c.AccountLabels["region"] = "eu" }},
		{"cooldown", func(c *Candidate) { value := now.Add(time.Minute); c.CooldownUntil = &value }},
		{"node", func(c *Candidate) { c.NodeConnectivity = "offline" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.AccountLabels = map[string]string{"region": "ap"}
			test.mutate(&candidate)
			if _, err := SelectCandidate(request, []Candidate{candidate}); !errors.Is(err, ErrNoEligibleCandidate) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	base.ReservedConcurrency = base.MaxConcurrency
	if _, err := SelectCandidate(request, []Candidate{base}); !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("capacity error=%v", err)
	}
}
