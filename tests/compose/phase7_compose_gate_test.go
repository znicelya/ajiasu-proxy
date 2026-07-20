package compose_test

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/gateways"
	"github.com/znicelya/ajiasu-proxy/internal/health"
	"github.com/znicelya/ajiasu-proxy/internal/scheduler"
	"github.com/google/uuid"
)

func TestRunnerAndCleanupOwnershipContractIsExact(t *testing.T) {
	runtimeSource, err := os.ReadFile("../../crates/agent/src/runtime/docker.rs")
	if err != nil {
		t.Fatal(err)
	}
	runtimeText := string(runtimeSource)
	for _, required := range []string{`"ajiasu.owner"`, `"control-plane"`, `"ajiasu.node_id"`, `"ajiasu.runner_id"`, `"ajiasu.tenant_id"`, `"ajiasu.endpoint_id"`, `"ajiasu.operation_id"`, `"ajiasu.generation"`, `network_mode: Some("none"`, `readonly_rootfs: Some(true)`, `cap_drop: Some(vec!["ALL"`, `memory: Some(256 * 1024 * 1024)`, `nano_cpus: Some(1_000_000_000)`} {
		if !strings.Contains(runtimeText, required) {
			t.Errorf("Runner ownership/security contract misses %q", required)
		}
	}
	down, err := os.ReadFile("../../scripts/compose-down.ps1")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	if !strings.Contains(downText, "label=ajiasu.owner=control-plane") || strings.Contains(downText, "--filter name=ajiasu-runner") {
		t.Fatal("shutdown cleanup is not exact-label based")
	}
}

func TestPreviousReleaseFixtureRequiresImmutableDatabaseBackedRollback(t *testing.T) {
	content, err := os.ReadFile("fixtures/previous-release.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion   int               `json:"schema_version"`
		Images          map[string]string `json:"images"`
		RequiresRestore bool              `json:"requires_database_restore_for_schema_change"`
	}
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	immutable := regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	if fixture.SchemaVersion != 11 || !fixture.RequiresRestore || len(fixture.Images) != 4 {
		t.Fatalf("fixture=%#v", fixture)
	}
	for name, image := range fixture.Images {
		if !immutable.MatchString(image) {
			t.Fatalf("mutable %s image %q", name, image)
		}
	}
}

func TestFixedAndEveryPoolStrategyRespectCapacityAndTenant(t *testing.T) {
	now := time.Now().UTC()
	tenant, pool := uuid.New(), uuid.New()
	candidates := []scheduler.Candidate{
		candidate(tenant, pool, 10, 4, 1, 20),
		candidate(tenant, pool, 20, 1, 2, 10),
		candidate(uuid.New(), pool, 1, 0, 0, 100),
	}
	for _, strategy := range []scheduler.Strategy{scheduler.StrategyLeastConnections, scheduler.StrategyRoundRobin, scheduler.StrategyFixedPriority} {
		selection, err := scheduler.SelectCandidate(scheduler.SelectionRequest{TenantID: tenant, PoolID: pool, Strategy: strategy, RoundRobinCursor: 1, Now: now}, candidates)
		if err != nil {
			t.Fatalf("strategy %s: %v", strategy, err)
		}
		if selection.Candidate.TenantID != tenant || selection.Candidate.ReservedConcurrency >= selection.Candidate.MaxConcurrency {
			t.Fatalf("unsafe selection for %s: %#v", strategy, selection)
		}
	}
	fixed := candidates[0]
	if fixed.TenantID != tenant || fixed.NodeConnectivity != "online" || fixed.NodeMaintenance != "active" {
		t.Fatal("fixed assignment fixture is not ready")
	}
	exhausted := candidates[:2]
	for index := range exhausted {
		exhausted[index].ReservedConcurrency = exhausted[index].MaxConcurrency
	}
	if _, err := scheduler.SelectCandidate(scheduler.SelectionRequest{TenantID: tenant, PoolID: pool, Strategy: scheduler.StrategyLeastConnections, Now: now}, exhausted); !errors.Is(err, scheduler.ErrCapacityExhausted) {
		t.Fatalf("oversell error=%v", err)
	}
}

func TestRestartReorderDrainAndCrossTenantIsolation(t *testing.T) {
	now := time.Now().UTC()
	tenant, endpoint, gatewayID := uuid.New(), uuid.New(), uuid.New()
	route := gateways.Route{TenantID: tenant, EndpointID: endpoint, PolicyHash: "policy", Protocols: []string{"http", "connect", "socks5"}, AssignmentID: uuid.New(), AssignmentGeneration: 4, AccountID: uuid.New(), NodeID: uuid.New(), AssignmentState: "assigned", ValidUntil: now.Add(time.Minute), Grant: gateways.RouteGrant{GatewayID: gatewayID, TenantID: tenant, EndpointID: endpoint, RunnerID: uuid.New(), Generation: 4, Protocols: []string{"http", "connect", "socks5"}, PolicyHash: "policy", ExpiresAt: now.Add(time.Minute), Signature: []byte{1}}}
	applier := gateways.NewSnapshotApplier()
	if err := applier.ApplySnapshot(gateways.Snapshot{Version: 7, GeneratedAt: now, Routes: []gateways.Route{route}}); err != nil {
		t.Fatal(err)
	}
	if err := applier.ApplySnapshot(gateways.Snapshot{Version: 7, GeneratedAt: now, Routes: []gateways.Route{route}}); err != nil {
		t.Fatalf("duplicate restart snapshot: %v", err)
	}
	if err := applier.ApplyDelta(gateways.Delta{Version: 6, Route: route}); !errors.Is(err, gateways.ErrStaleVersion) {
		t.Fatalf("reordered event=%v", err)
	}
	if _, ok := applier.Route(uuid.New(), endpoint); ok {
		t.Fatal("cross-tenant route visible")
	}
	route.AssignmentState = "draining"
	if err := applier.ApplyDelta(gateways.Delta{Version: 8, Route: route}); err != nil {
		t.Fatal(err)
	}
	if _, ok := applier.Route(tenant, endpoint); !ok {
		t.Fatal("established draining route disappeared before finalizer")
	}
}

func TestQuarantineReplacementRequiresNewFenceAndPublishedRoute(t *testing.T) {
	now := time.Now().UTC()
	tenant, account := uuid.New(), uuid.New()
	var state health.State
	for sequence := uint64(1); sequence <= 3; sequence++ {
		state, _, _ = health.Evaluate(state, health.Observation{TenantID: tenant, ResourceType: "account", ResourceID: account, Dimension: health.DimensionAccount, Result: health.ResultFailure, Generation: 1, Sequence: sequence, ReasonCode: "authentication_failed", ObservedAt: now}, health.DefaultConfig())
	}
	if state.Status != health.StatusQuarantined {
		t.Fatalf("state=%s", state.Status)
	}
	assignment := scheduler.Assignment{AssignmentID: uuid.New(), EndpointID: uuid.New(), AccountID: &account, DesiredGeneration: 2, FencingToken: 4}
	plan, err := scheduler.PlanMigration(assignment, "pool", scheduler.Failure{Class: scheduler.FailureAccount, ReasonCode: "account_quarantined", ObservedAt: now}, 5, 1, scheduler.DefaultMigrationPolicy())
	if err != nil {
		t.Fatal(err)
	}
	observation := scheduler.ReplacementObservation{AssignmentID: plan.AssignmentID, RunnerID: uuid.New(), Generation: plan.DesiredGeneration, FencingToken: plan.FencingToken, Running: true}
	if !errors.Is(scheduler.ValidateReplacement(plan, observation), scheduler.ErrReplacementNotReady) {
		t.Fatal("unpublished replacement accepted")
	}
	observation.RoutePublished = true
	if err := scheduler.ValidateReplacement(plan, observation); err != nil {
		t.Fatal(err)
	}
}

func candidate(tenant, pool uuid.UUID, priority, reserved int, active int64, weight int) scheduler.Candidate {
	return scheduler.Candidate{TenantID: tenant, PoolID: pool, MembershipID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New(), AccountState: "active", AccountHealth: scheduler.HealthHealthy, NodeMaintenance: "active", NodeConnectivity: "online", MembershipEnabled: true, Priority: priority, Weight: weight, MaxConcurrency: 5, ReservedConcurrency: reserved, ActiveConnections: active, NodeMaxRunners: 10, NodeReservedHeadroom: 1, NodeActiveRunners: 1}
}
