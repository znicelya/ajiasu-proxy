package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver/dbgen"
	"github.com/znicelya/ajiasu-proxy/internal/platform/keyring"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type IdempotencyScope string

const (
	IdempotencyScopePlatform IdempotencyScope = "platform"
	IdempotencyScopeTenant   IdempotencyScope = "tenant"
	defaultIdempotencyTTL                     = 24 * time.Hour
)

var (
	ErrIdempotencyInvalid  = errors.New("invalid idempotency request")
	ErrIdempotencyRequired = errors.New("idempotency key is required")
	ErrIdempotencyConflict = errors.New("idempotency key conflicts with an earlier request")
)

type IdempotencyRequest struct {
	Scope           IdempotencyScope
	TenantID        *uuid.UUID
	ActorID         uuid.UUID
	Method          string
	CanonicalRoute  string
	Key             string
	Body            []byte
	ProtectResponse bool
}

type StoredResponse struct {
	Status int
	Body   []byte
}

func WriteStoredResponse(w http.ResponseWriter, response StoredResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}

type IdempotencyStore struct {
	pools     *database.Pools
	now       func() time.Time
	newID     func() (uuid.UUID, error)
	ttl       time.Duration
	protector keyring.Keyring
}

func NewIdempotencyStore(pools *database.Pools, protectors ...keyring.Keyring) (*IdempotencyStore, error) {
	if pools == nil || pools.Platform == nil || pools.Tenant == nil {
		return nil, ErrIdempotencyInvalid
	}
	var protector keyring.Keyring
	if len(protectors) > 1 {
		return nil, ErrIdempotencyInvalid
	}
	if len(protectors) == 1 {
		protector = protectors[0]
	}
	return &IdempotencyStore{
		pools:     pools,
		now:       func() time.Time { return time.Now().UTC() },
		newID:     uuid.NewV7,
		ttl:       defaultIdempotencyTTL,
		protector: protector,
	}, nil
}

func HashRequestBody(body []byte) [32]byte { return sha256.Sum256(body) }

func CanonicalRoute(r *http.Request) string {
	if r == nil {
		return ""
	}
	if routeContext := r.Context().Value(routePatternContextKey{}); routeContext != nil {
		if route, ok := routeContext.(string); ok {
			return route
		}
	}
	return r.URL.Path
}

type routePatternContextKey struct{}

func WithCanonicalRoute(ctx context.Context, route string) context.Context {
	return context.WithValue(ctx, routePatternContextKey{}, route)
}

func (s *IdempotencyStore) Execute(
	ctx context.Context,
	request IdempotencyRequest,
	operation func(context.Context, pgx.Tx) (StoredResponse, error),
) (StoredResponse, bool, error) {
	if err := validateIdempotencyRequest(request); err != nil {
		return StoredResponse{}, false, err
	}
	if operation == nil {
		return StoredResponse{}, false, ErrIdempotencyInvalid
	}
	now := s.now().UTC()
	id, err := s.newID()
	if err != nil {
		return StoredResponse{}, false, err
	}
	hash := HashRequestBody(request.Body)
	run := func(ctx context.Context, tx pgx.Tx) (idempotencyResult, error) {
		return s.executeTx(ctx, tx, request, id, hash, now, operation)
	}
	var result idempotencyResult
	if request.Scope == IdempotencyScopePlatform {
		result, err = database.InPlatformTx(ctx, s.pools.Platform, request.ActorID, run)
	} else {
		result, err = database.InTenantTx(ctx, s.pools.Tenant, *request.TenantID, request.ActorID, run)
	}
	if err != nil {
		return StoredResponse{}, false, err
	}
	return result.response, result.replayed, nil
}

func (s *IdempotencyStore) ExecuteJSON(
	ctx context.Context,
	request IdempotencyRequest,
	operation func(context.Context) (int, any, error),
) (StoredResponse, bool, error) {
	if operation == nil {
		return StoredResponse{}, false, ErrIdempotencyInvalid
	}
	return s.Execute(ctx, request, func(ctx context.Context, _ pgx.Tx) (StoredResponse, error) {
		status, value, err := operation(ctx)
		if err != nil {
			return StoredResponse{}, err
		}
		body, err := json.Marshal(value)
		if err != nil {
			return StoredResponse{}, err
		}
		return StoredResponse{Status: status, Body: body}, nil
	})
}

type idempotencyResult struct {
	response StoredResponse
	replayed bool
}

func (s *IdempotencyStore) executeTx(
	ctx context.Context,
	tx pgx.Tx,
	request IdempotencyRequest,
	id uuid.UUID,
	hash [32]byte,
	now time.Time,
	operation func(context.Context, pgx.Tx) (StoredResponse, error),
) (idempotencyResult, error) {
	queries := dbgen.New(tx)
	params := dbgen.ReserveIdempotencyRecordParams{
		ID:             id,
		Scope:          string(request.Scope),
		TenantID:       cloneUUIDPointer(request.TenantID),
		ActorID:        request.ActorID,
		Method:         strings.ToUpper(request.Method),
		CanonicalRoute: request.CanonicalRoute,
		IdempotencyKey: request.Key,
		RequestHash:    hash[:],
		CreatedAt:      now,
		ExpiresAt:      now.Add(s.ttl),
	}
	record, err := queries.ReserveIdempotencyRecord(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		record, err = queries.GetIdempotencyRecordForUpdate(ctx, dbgen.GetIdempotencyRecordForUpdateParams{
			Scope: params.Scope, TenantID: params.TenantID, ActorID: params.ActorID,
			Method: params.Method, CanonicalRoute: params.CanonicalRoute, IdempotencyKey: params.IdempotencyKey,
		})
	}
	if err != nil {
		return idempotencyResult{}, err
	}
	if subtle.ConstantTimeCompare(record.RequestHash, hash[:]) != 1 {
		return idempotencyResult{}, ErrIdempotencyConflict
	}
	if record.ResponseStatus.Valid {
		body := append([]byte(nil), record.ResponseBody...)
		if record.ResponseProtected {
			if !request.ProtectResponse || s.protector == nil {
				return idempotencyResult{}, ErrIdempotencyConflict
			}
			body, err = s.protector.Decrypt(body, idempotencyResponseContext(record.ID))
			if err != nil {
				return idempotencyResult{}, err
			}
		} else if request.ProtectResponse {
			return idempotencyResult{}, ErrIdempotencyConflict
		}
		return idempotencyResult{response: StoredResponse{Status: int(record.ResponseStatus.Int32), Body: body}, replayed: true}, nil
	}
	operationContext := database.ContextWithPlatformTx(ctx, tx, request.ActorID)
	if request.Scope == IdempotencyScopeTenant {
		operationContext = database.ContextWithTenantTx(ctx, tx, *request.TenantID, request.ActorID)
	}
	response, err := operation(operationContext, tx)
	if err != nil {
		return idempotencyResult{}, err
	}
	if response.Status < 200 || response.Status > 599 || response.Body == nil || len(response.Body) > maxRequestBodyBytes {
		return idempotencyResult{}, ErrIdempotencyInvalid
	}
	storedBody := append([]byte(nil), response.Body...)
	if request.ProtectResponse {
		if s.protector == nil {
			return idempotencyResult{}, ErrIdempotencyInvalid
		}
		storedBody, err = s.protector.Encrypt(storedBody, idempotencyResponseContext(record.ID))
		if err != nil {
			return idempotencyResult{}, err
		}
	}
	completedAt := s.now().UTC()
	if _, err := queries.CompleteIdempotencyRecord(ctx, dbgen.CompleteIdempotencyRecordParams{
		ResponseStatus:    pgtype.Int4{Int32: int32(response.Status), Valid: true},
		ResponseBody:      storedBody,
		ResponseProtected: request.ProtectResponse,
		CompletedAt:       &completedAt,
		ID:                record.ID,
	}); err != nil {
		return idempotencyResult{}, err
	}
	return idempotencyResult{response: StoredResponse{Status: response.Status, Body: append([]byte(nil), response.Body...)}}, nil
}

func idempotencyResponseContext(id uuid.UUID) []byte {
	return []byte("idempotency-response:v1:" + id.String())
}

func validateIdempotencyRequest(request IdempotencyRequest) error {
	request.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	request.Key = strings.TrimSpace(request.Key)
	if request.ProtectResponse && request.Scope != IdempotencyScopePlatform && request.Scope != IdempotencyScopeTenant {
		return ErrIdempotencyInvalid
	}
	if request.ActorID == uuid.Nil || request.Key == "" || len(request.Key) > 255 || request.Method == "" || len(request.Method) > 16 ||
		!strings.HasPrefix(request.CanonicalRoute, "/api/v1/") || len(request.CanonicalRoute) > 512 {
		if request.Key == "" {
			return ErrIdempotencyRequired
		}
		return ErrIdempotencyInvalid
	}
	switch request.Scope {
	case IdempotencyScopePlatform:
		if request.TenantID != nil {
			return ErrIdempotencyInvalid
		}
	case IdempotencyScopeTenant:
		if request.TenantID == nil || *request.TenantID == uuid.Nil {
			return ErrIdempotencyInvalid
		}
	default:
		return ErrIdempotencyInvalid
	}
	return nil
}

func cloneUUIDPointer(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
