package integration

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/gateways"
	"github.com/znicelya/ajiasu-proxy/internal/health"
	"github.com/znicelya/ajiasu-proxy/internal/scheduler"
	"github.com/google/uuid"
)

type contentionRedis struct {
	mu     sync.Mutex
	down   bool
	leases map[string]string
	fences map[string]int64
}

func newContentionRedis() *contentionRedis {
	return &contentionRedis{leases: map[string]string{}, fences: map[string]int64{}}
}

func (redis *contentionRedis) Eval(_ context.Context, script string, keys []string, args ...string) ([]int64, error) {
	redis.mu.Lock()
	defer redis.mu.Unlock()
	if redis.down {
		return nil, errors.New("coordination unavailable")
	}
	switch {
	case strings.Contains(script, "lease:acquire"):
		if current := redis.leases[keys[0]]; current != "" {
			parts := strings.Split(current, ":")
			token, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
			return []int64{0, token}, nil
		}
		redis.fences[keys[1]]++
		token := redis.fences[keys[1]]
		redis.leases[keys[0]] = args[0] + ":" + strconv.FormatInt(token, 10)
		return []int64{1, token}, nil
	case strings.Contains(script, "lease:release"):
		expected := args[0] + ":" + args[1]
		if redis.leases[keys[0]] != expected {
			return []int64{0}, nil
		}
		delete(redis.leases, keys[0])
		return []int64{1}, nil
	case strings.Contains(script, "lease:renew"):
		expected := args[0] + ":" + args[1]
		if redis.leases[keys[0]] != expected {
			return []int64{0}, nil
		}
		return []int64{1}, nil
	default:
		return nil, errors.New("unknown script")
	}
}

func (redis *contentionRedis) setDown(down bool) {
	redis.mu.Lock()
	defer redis.mu.Unlock()
	redis.down = down
}

func TestPhase6CompetingSchedulersFenceOneOwnerAndRecover(t *testing.T) {
	backend := newContentionRedis()
	tenantID, endpointID := uuid.New(), uuid.New()
	resource := []scheduler.ResourceKey{{Kind: "endpoint", TenantID: tenantID, ResourceID: endpointID}}
	config := scheduler.LeaseConfig{Namespace: "phase6:test", TTL: 9 * time.Second, RenewInterval: 2 * time.Second, Timeout: time.Second}
	type result struct {
		manager *scheduler.LeaseManager
		leases  []scheduler.Lease
		err     error
	}
	const replicas = 32
	start := make(chan struct{})
	results := make(chan result, replicas)
	for index := 0; index < replicas; index++ {
		manager, err := scheduler.NewLeaseManager(backend, config, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		go func(manager *scheduler.LeaseManager) {
			<-start
			leases, acquireErr := manager.Acquire(t.Context(), resource)
			results <- result{manager: manager, leases: leases, err: acquireErr}
		}(manager)
	}
	close(start)
	var winner result
	busy := 0
	for index := 0; index < replicas; index++ {
		current := <-results
		if current.err == nil {
			if winner.manager != nil {
				t.Fatal("two scheduler replicas acquired the same endpoint")
			}
			winner = current
		} else if errors.Is(current.err, scheduler.ErrLeaseBusy) {
			busy++
		} else {
			t.Fatalf("unexpected contention error: %v", current.err)
		}
	}
	if winner.manager == nil || busy != replicas-1 || winner.leases[0].FencingToken != 1 {
		t.Fatalf("winner=%+v busy=%d", winner.leases, busy)
	}
	if err := winner.manager.Release(t.Context(), winner.leases); err != nil {
		t.Fatal(err)
	}
	next, _ := scheduler.NewLeaseManager(backend, config, uuid.New())
	nextLeases, err := next.Acquire(t.Context(), resource)
	if err != nil || nextLeases[0].FencingToken <= winner.leases[0].FencingToken {
		t.Fatalf("reacquire leases=%+v err=%v", nextLeases, err)
	}
	if !errors.Is(scheduler.CheckFencingToken(nextLeases[0].FencingToken, winner.leases[0].FencingToken), scheduler.ErrStaleFencingToken) {
		t.Fatal("stale fencing token was accepted")
	}
	backend.setDown(true)
	other := []scheduler.ResourceKey{{Kind: "endpoint", TenantID: tenantID, ResourceID: uuid.New()}}
	if _, err := next.Acquire(t.Context(), other); !errors.Is(err, scheduler.ErrCoordinationDown) {
		t.Fatalf("degraded acquire error=%v", err)
	}
	backend.setDown(false)
	if recovered, err := next.Acquire(t.Context(), other); err != nil || len(recovered) != 1 {
		t.Fatalf("recovered leases=%+v err=%v", recovered, err)
	}
}

func TestPhase6HealthMigrationAndRouteIsolationGate(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	tenantID, accountID := uuid.New(), uuid.New()
	observation := health.Observation{TenantID: tenantID, ResourceType: "account", ResourceID: accountID, Dimension: health.DimensionAccount, Result: health.ResultFailure, Generation: 1, ReasonCode: "authentication_failed", ObservedAt: now}
	var state health.State
	for sequence := uint64(1); sequence <= 3; sequence++ {
		observation.Sequence = sequence
		state, _, _ = health.Evaluate(state, observation, health.DefaultConfig())
	}
	if state.Status != health.StatusQuarantined {
		t.Fatalf("health state=%+v", state)
	}
	assignment := scheduler.Assignment{AssignmentID: uuid.New(), EndpointID: uuid.New(), AccountID: &accountID, DesiredGeneration: 3, FencingToken: 7}
	plan, err := scheduler.PlanMigration(assignment, "pool", scheduler.Failure{Class: scheduler.FailureAccount, ReasonCode: "account_quarantined", ObservedAt: now}, 8, 1, scheduler.DefaultMigrationPolicy())
	if err != nil || plan.Type != scheduler.MigrationAccountReplacement {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	replacement := scheduler.ReplacementObservation{AssignmentID: plan.AssignmentID, RunnerID: uuid.New(), Generation: plan.DesiredGeneration, FencingToken: plan.FencingToken, Running: true}
	if !errors.Is(scheduler.ValidateReplacement(plan, replacement), scheduler.ErrReplacementNotReady) {
		t.Fatal("replacement without a published route was accepted")
	}
	replacement.RoutePublished = true
	if err := scheduler.ValidateReplacement(plan, replacement); err != nil {
		t.Fatal(err)
	}

	// A Gateway cache key contains both tenant and endpoint IDs. A route from
	// one tenant cannot be selected through another tenant's scope.
	otherTenant := uuid.New()
	applier := gateways.NewSnapshotApplier()
	route := gateways.Route{TenantID: tenantID, EndpointID: assignment.EndpointID, PolicyHash: "hash", Protocols: []string{"connect"}, AssignmentID: assignment.AssignmentID, AssignmentGeneration: uint64(plan.DesiredGeneration), AccountID: accountID, NodeID: uuid.New(), AssignmentState: "assigned", ValidUntil: now.Add(time.Minute), Grant: gateways.RouteGrant{GatewayID: uuid.New(), TenantID: tenantID, EndpointID: assignment.EndpointID, RunnerID: replacement.RunnerID, Generation: uint64(plan.DesiredGeneration), Protocols: []string{"connect"}, PolicyHash: "hash", ExpiresAt: now.Add(time.Minute), Signature: []byte{1}}}
	if err := applier.ApplySnapshot(gateways.Snapshot{Version: 1, GeneratedAt: now, Routes: []gateways.Route{route}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := applier.Route(otherTenant, route.EndpointID); ok {
		t.Fatal("cross-tenant route became visible")
	}
	if err := applier.ApplyDelta(gateways.Delta{Version: 1, Route: route}); err != nil {
		t.Fatalf("duplicate delta: %v", err)
	}
	if err := applier.ApplyDelta(gateways.Delta{Version: 0, Route: route}); !errors.Is(err, gateways.ErrStaleVersion) {
		t.Fatalf("reordered delta error=%v", err)
	}
	established := route
	route.AssignmentState = "draining"
	if err := applier.ApplyDelta(gateways.Delta{Version: 2, Route: route}); err != nil {
		t.Fatal(err)
	}
	if _, err := applier.Select(tenantID, route.EndpointID, now); !errors.Is(err, gateways.ErrRouteUnavailable) {
		t.Fatalf("draining select error=%v", err)
	}
	if established.AssignmentState != "assigned" {
		t.Fatal("established stream route was mutated")
	}
}
