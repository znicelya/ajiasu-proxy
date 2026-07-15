package gateways

import (
	"context"
	"errors"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/google/uuid"
)

type UsageDelta struct {
	TenantID          uuid.UUID
	EndpointID        uuid.UUID
	CredentialID      *uuid.UUID
	WindowStart       time.Time
	WindowSeconds     int32
	ActiveConnections int32
	ConnectionCount   int64
	BytesIn           int64
	BytesOut          int64
}

var ErrUsageInvalid = errors.New("invalid usage delta")

type UsageAggregator struct{ deltas map[usageKey]UsageDelta }
type usageKey struct {
	tenantID, endpointID uuid.UUID
	credentialID         uuid.UUID
	windowStart          time.Time
}

func NewUsageAggregator() *UsageAggregator {
	return &UsageAggregator{deltas: map[usageKey]UsageDelta{}}
}
func (a *UsageAggregator) Add(delta UsageDelta) error {
	if delta.TenantID == uuid.Nil || delta.EndpointID == uuid.Nil || delta.WindowStart.IsZero() || delta.WindowSeconds <= 0 || delta.ActiveConnections < 0 || delta.ConnectionCount < 0 || delta.BytesIn < 0 || delta.BytesOut < 0 {
		return ErrUsageInvalid
	}
	var credentialID uuid.UUID
	if delta.CredentialID != nil {
		credentialID = *delta.CredentialID
	}
	key := usageKey{delta.TenantID, delta.EndpointID, credentialID, delta.WindowStart.UTC()}
	current := a.deltas[key]
	if current.TenantID == uuid.Nil {
		current = delta
		current.WindowStart = key.windowStart
	} else {
		current.ActiveConnections += delta.ActiveConnections
		current.ConnectionCount += delta.ConnectionCount
		current.BytesIn += delta.BytesIn
		current.BytesOut += delta.BytesOut
	}
	a.deltas[key] = current
	return nil
}
func (a *UsageAggregator) Drain() []UsageDelta {
	values := make([]UsageDelta, 0, len(a.deltas))
	for _, value := range a.deltas {
		values = append(values, value)
	}
	a.deltas = map[usageKey]UsageDelta{}
	return values
}
func FlushUsage(ctx context.Context, pool *database.Pools, deltas []UsageDelta) error {
	if pool == nil || pool.Tenant == nil {
		return ErrUsageInvalid
	}
	for _, delta := range deltas {
		var credential any
		if delta.CredentialID != nil {
			credential = *delta.CredentialID
		}
		_, err := pool.Tenant.Exec(ctx, `INSERT INTO gateways.usage_windows (tenant_id,endpoint_id,credential_id,window_start,window_seconds,active_connections,connection_count,bytes_in,bytes_out,version,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,now()) ON CONFLICT (tenant_id,endpoint_id,credential_id,window_start) DO UPDATE SET active_connections=gateways.usage_windows.active_connections+EXCLUDED.active_connections,connection_count=gateways.usage_windows.connection_count+EXCLUDED.connection_count,bytes_in=gateways.usage_windows.bytes_in+EXCLUDED.bytes_in,bytes_out=gateways.usage_windows.bytes_out+EXCLUDED.bytes_out,version=gateways.usage_windows.version+1,updated_at=now()`, delta.TenantID, delta.EndpointID, credential, delta.WindowStart.UTC(), delta.WindowSeconds, delta.ActiveConnections, delta.ConnectionCount, delta.BytesIn, delta.BytesOut)
		if err != nil {
			return err
		}
	}
	return nil
}
