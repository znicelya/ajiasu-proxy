package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrLeaseInvalid      = errors.New("scheduler lease request is invalid")
	ErrLeaseBusy         = errors.New("scheduler lease is owned by another replica")
	ErrLeaseLost         = errors.New("scheduler lease ownership was lost")
	ErrCoordinationDown  = errors.New("scheduler coordination is unavailable")
	ErrStaleFencingToken = errors.New("scheduler fencing token is stale")
)

const AcquireLeaseLua = `-- ajiasu:lease:acquire:v1
local lease_key = KEYS[1]
local fence_key = KEYS[2]
local owner = ARGV[1]
local ttl = tonumber(ARGV[2])
local current = redis.call('GET', lease_key)
if current then
  local separator = string.find(current, ':')
  if not separator then return {-2, 0} end
  local current_owner = string.sub(current, 1, separator - 1)
  local token = tonumber(string.sub(current, separator + 1))
  if current_owner == owner then
    redis.call('PEXPIRE', lease_key, ttl)
    return {2, token}
  end
  return {0, token}
end
local token = redis.call('INCR', fence_key)
local stored = redis.call('SET', lease_key, owner .. ':' .. token, 'PX', ttl, 'NX')
if not stored then return {0, token} end
return {1, token}`

const RenewLeaseLua = `-- ajiasu:lease:renew:v1
local current = redis.call('GET', KEYS[1])
local expected = ARGV[1] .. ':' .. ARGV[2]
if current ~= expected then return 0 end
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[3]))
return 1`

const ReleaseLeaseLua = `-- ajiasu:lease:release:v1
local current = redis.call('GET', KEYS[1])
local expected = ARGV[1] .. ':' .. ARGV[2]
if current ~= expected then return 0 end
return redis.call('DEL', KEYS[1])`

type RedisEvaluator interface {
	Eval(context.Context, string, []string, ...string) ([]int64, error)
}

type LeaseConfig struct {
	Namespace     string
	TTL           time.Duration
	RenewInterval time.Duration
	Timeout       time.Duration
}

func (c LeaseConfig) Validate() error {
	if strings.TrimSpace(c.Namespace) == "" || c.TTL < 3*time.Second || c.TTL > 5*time.Minute || c.RenewInterval <= 0 || c.RenewInterval >= c.TTL/2 || c.Timeout <= 0 || c.Timeout > c.RenewInterval {
		return ErrLeaseInvalid
	}
	return nil
}

type ResourceKey struct {
	Kind       string
	TenantID   uuid.UUID
	ResourceID uuid.UUID
}

func (key ResourceKey) canonical(namespace string) (string, error) {
	if key.ResourceID == uuid.Nil {
		return "", ErrLeaseInvalid
	}
	switch key.Kind {
	case "endpoint", "pool", "account":
		if key.TenantID == uuid.Nil {
			return "", ErrLeaseInvalid
		}
		return fmt.Sprintf("%s:%s:%s:%s", namespace, key.Kind, key.TenantID, key.ResourceID), nil
	case "node":
		if key.TenantID != uuid.Nil {
			return "", ErrLeaseInvalid
		}
		return fmt.Sprintf("%s:node:%s", namespace, key.ResourceID), nil
	default:
		return "", ErrLeaseInvalid
	}
}

type Lease struct {
	Key          string
	OwnerID      uuid.UUID
	FencingToken uint64
	ExpiresAt    time.Time
}

type LeaseManager struct {
	redis   RedisEvaluator
	config  LeaseConfig
	ownerID uuid.UUID
	now     func() time.Time
}

func NewLeaseManager(redis RedisEvaluator, config LeaseConfig, ownerID uuid.UUID) (*LeaseManager, error) {
	if redis == nil || ownerID == uuid.Nil || config.Validate() != nil {
		return nil, ErrLeaseInvalid
	}
	return &LeaseManager{redis: redis, config: config, ownerID: ownerID, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (manager *LeaseManager) Acquire(ctx context.Context, resources []ResourceKey) ([]Lease, error) {
	if len(resources) == 0 || len(resources) > 32 {
		return nil, ErrLeaseInvalid
	}
	keys := make([]string, 0, len(resources))
	seen := map[string]bool{}
	for _, resource := range resources {
		key, err := resource.canonical(manager.config.Namespace)
		if err != nil || seen[key] {
			return nil, ErrLeaseInvalid
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	leases := make([]Lease, 0, len(keys))
	for _, key := range keys {
		lease, err := manager.acquireOne(ctx, key)
		if err != nil {
			_ = manager.Release(context.WithoutCancel(ctx), leases)
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, nil
}

func (manager *LeaseManager) acquireOne(ctx context.Context, key string) (Lease, error) {
	result, err := manager.eval(ctx, AcquireLeaseLua, []string{key, key + ":fence"}, manager.ownerID.String(), strconv.FormatInt(manager.config.TTL.Milliseconds(), 10))
	if err != nil {
		return Lease{}, err
	}
	if len(result) != 2 || result[0] < 0 || result[1] <= 0 {
		return Lease{}, ErrCoordinationDown
	}
	if result[0] == 0 {
		return Lease{}, ErrLeaseBusy
	}
	return Lease{Key: key, OwnerID: manager.ownerID, FencingToken: uint64(result[1]), ExpiresAt: manager.now().Add(manager.config.TTL)}, nil
}

func (manager *LeaseManager) Renew(ctx context.Context, leases []Lease) error {
	for index := range leases {
		lease := &leases[index]
		if lease.OwnerID != manager.ownerID || lease.FencingToken == 0 {
			return ErrLeaseInvalid
		}
		result, err := manager.eval(ctx, RenewLeaseLua, []string{lease.Key}, manager.ownerID.String(), strconv.FormatUint(lease.FencingToken, 10), strconv.FormatInt(manager.config.TTL.Milliseconds(), 10))
		if err != nil {
			return err
		}
		if len(result) != 1 || result[0] != 1 {
			return ErrLeaseLost
		}
		lease.ExpiresAt = manager.now().Add(manager.config.TTL)
	}
	return nil
}

func (manager *LeaseManager) Release(ctx context.Context, leases []Lease) error {
	var releaseErr error
	for index := len(leases) - 1; index >= 0; index-- {
		lease := leases[index]
		if lease.OwnerID != manager.ownerID || lease.FencingToken == 0 {
			releaseErr = errors.Join(releaseErr, ErrLeaseInvalid)
			continue
		}
		result, err := manager.eval(ctx, ReleaseLeaseLua, []string{lease.Key}, manager.ownerID.String(), strconv.FormatUint(lease.FencingToken, 10))
		if err != nil {
			releaseErr = errors.Join(releaseErr, err)
			continue
		}
		if len(result) != 1 || (result[0] != 0 && result[0] != 1) {
			releaseErr = errors.Join(releaseErr, ErrCoordinationDown)
		}
	}
	return releaseErr
}

func (manager *LeaseManager) eval(parent context.Context, script string, keys []string, args ...string) ([]int64, error) {
	ctx, cancel := context.WithTimeout(parent, manager.config.Timeout)
	defer cancel()
	result, err := manager.redis.Eval(ctx, script, keys, args...)
	if err != nil {
		return nil, ErrCoordinationDown
	}
	return result, nil
}

func CheckFencingToken(current, incoming uint64) error {
	if incoming == 0 || incoming < current {
		return ErrStaleFencingToken
	}
	return nil
}
