package audit

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit/dbgen"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrReadInvalid = errors.New("invalid audit read request")

type EventSummary struct {
	ID           uuid.UUID
	TenantID     *uuid.UUID
	ActorType    string
	ActorID      *uuid.UUID
	Action       string
	ResourceType string
	ResourceID   *uuid.UUID
	Result       string
	SourceIP     netip.Addr
	UserAgent    string
	RequestID    uuid.UUID
	CreatedAt    time.Time
}

type ReadRequest struct {
	ActorID  uuid.UUID
	TenantID *uuid.UUID
	Platform bool
	After    time.Time
	AfterID  uuid.UUID
	PageSize int32
}

type Reader struct{ pools *database.Pools }

func NewReader(pools *database.Pools) (*Reader, error) {
	if pools == nil || pools.Platform == nil || pools.Tenant == nil {
		return nil, ErrReadInvalid
	}
	return &Reader{pools: pools}, nil
}

func (r *Reader) List(ctx context.Context, request ReadRequest) ([]EventSummary, error) {
	if request.ActorID == uuid.Nil || request.PageSize < 1 || request.PageSize > 200 || !request.Platform && (request.TenantID == nil || *request.TenantID == uuid.Nil) {
		return nil, ErrReadInvalid
	}
	list := func(ctx context.Context, tx pgx.Tx) ([]dbgen.ListAuditEventsRow, error) {
		return dbgen.New(tx).ListAuditEvents(ctx, dbgen.ListAuditEventsParams{
			TenantID: request.TenantID, AfterCreatedAt: request.After, AfterID: request.AfterID, PageSize: request.PageSize,
		})
	}
	var rows []dbgen.ListAuditEventsRow
	var err error
	if request.Platform {
		rows, err = database.InPlatformTx(ctx, r.pools.Platform, request.ActorID, list)
	} else {
		rows, err = database.InTenantTx(ctx, r.pools.Tenant, *request.TenantID, request.ActorID, list)
	}
	if err != nil {
		return nil, err
	}
	result := make([]EventSummary, len(rows))
	for index := range rows {
		row := rows[index]
		result[index] = EventSummary{
			ID: row.ID, TenantID: cloneUUID(row.TenantID), ActorType: row.ActorType, ActorID: cloneUUID(row.ActorID),
			Action: row.Action, ResourceType: row.ResourceType, ResourceID: cloneUUID(row.ResourceID), Result: row.Result,
			SourceIP: row.SourceIp, UserAgent: row.UserAgent, RequestID: row.RequestID, CreatedAt: row.CreatedAt.UTC(),
		}
	}
	return result, nil
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
