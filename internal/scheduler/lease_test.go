package scheduler

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeRedis struct {
	mu      sync.Mutex
	leasing map[string]string
	fences  map[string]int64
	fail    bool
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{leasing: map[string]string{}, fences: map[string]int64{}}
}

func (redis *fakeRedis) Eval(_ context.Context, script string, keys []string, args ...string) ([]int64, error) {
	redis.mu.Lock()
	defer redis.mu.Unlock()
	if redis.fail {
		return nil, errors.New("redis unavailable")
	}
	switch {
	case strings.Contains(script, "lease:acquire"):
		if current := redis.leasing[keys[0]]; current != "" {
			parts := strings.Split(current, ":")
			token, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
			owner := strings.Join(parts[:len(parts)-1], ":")
			if owner == args[0] {
				return []int64{2, token}, nil
			}
			return []int64{0, token}, nil
		}
		redis.fences[keys[1]]++
		token := redis.fences[keys[1]]
		redis.leasing[keys[0]] = args[0] + ":" + strconv.FormatInt(token, 10)
		return []int64{1, token}, nil
	case strings.Contains(script, "lease:renew"):
		expected := args[0] + ":" + args[1]
		if redis.leasing[keys[0]] != expected {
			return []int64{0}, nil
		}
		return []int64{1}, nil
	case strings.Contains(script, "lease:release"):
		expected := args[0] + ":" + args[1]
		if redis.leasing[keys[0]] != expected {
			return []int64{0}, nil
		}
		delete(redis.leasing, keys[0])
		return []int64{1}, nil
	default:
		return nil, errors.New("unknown script")
	}
}

func testLeaseConfig() LeaseConfig {
	return LeaseConfig{Namespace: "ajiasu:lease:v1", TTL: 9 * time.Second, RenewInterval: 2 * time.Second, Timeout: time.Second}
}

func TestLeaseManagerOrdersResourcesAndFencesCompetingOwners(t *testing.T) {
	backend := newFakeRedis()
	first, _ := NewLeaseManager(backend, testLeaseConfig(), uuid.New())
	second, _ := NewLeaseManager(backend, testLeaseConfig(), uuid.New())
	tenant, endpoint, account := uuid.New(), uuid.New(), uuid.New()
	resources := []ResourceKey{{Kind: "account", TenantID: tenant, ResourceID: account}, {Kind: "endpoint", TenantID: tenant, ResourceID: endpoint}}
	leases, err := first.Acquire(t.Context(), resources)
	if err != nil {
		t.Fatal(err)
	}
	if leases[0].Key > leases[1].Key {
		t.Fatalf("leases are not sorted: %+v", leases)
	}
	if _, err := second.Acquire(t.Context(), resources); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("error=%v", err)
	}
	if err := first.Release(t.Context(), leases); err != nil {
		t.Fatal(err)
	}
	newLeases, err := second.Acquire(t.Context(), resources)
	if err != nil {
		t.Fatal(err)
	}
	if newLeases[0].FencingToken <= leases[0].FencingToken {
		t.Fatalf("token did not increase old=%d new=%d", leases[0].FencingToken, newLeases[0].FencingToken)
	}
}

func TestLeaseRenewAndReleaseCannotAffectNewerOwner(t *testing.T) {
	backend := newFakeRedis()
	owner1, owner2 := uuid.New(), uuid.New()
	first, _ := NewLeaseManager(backend, testLeaseConfig(), owner1)
	second, _ := NewLeaseManager(backend, testLeaseConfig(), owner2)
	resource := ResourceKey{Kind: "node", ResourceID: uuid.New()}
	old, err := first.Acquire(t.Context(), []ResourceKey{resource})
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	delete(backend.leasing, old[0].Key)
	backend.mu.Unlock()
	current, err := second.Acquire(t.Context(), []ResourceKey{resource})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Renew(t.Context(), old); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew error=%v", err)
	}
	if err := first.Release(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := second.Renew(t.Context(), current); err != nil {
		t.Fatalf("new owner lost lease: %v", err)
	}
}

func TestLeaseManagerFailsClosedAndChecksFencing(t *testing.T) {
	backend := newFakeRedis()
	manager, _ := NewLeaseManager(backend, testLeaseConfig(), uuid.New())
	backend.fail = true
	if _, err := manager.Acquire(t.Context(), []ResourceKey{{Kind: "node", ResourceID: uuid.New()}}); !errors.Is(err, ErrCoordinationDown) {
		t.Fatalf("error=%v", err)
	}
	if err := CheckFencingToken(10, 9); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("error=%v", err)
	}
	if err := CheckFencingToken(10, 10); err != nil {
		t.Fatal(err)
	}
}
