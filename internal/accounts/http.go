package accounts

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/httpserver"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/requestctx"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type HTTPHandler struct {
	service     *Service
	idempotency *httpserver.IdempotencyStore
}

func NewHTTPHandler(service *Service, idempotency *httpserver.IdempotencyStore) (*HTTPHandler, error) {
	if service == nil || idempotency == nil {
		return nil, ErrInvalidArgument
	}
	return &HTTPHandler{service: service, idempotency: idempotency}, nil
}
func (h *HTTPHandler) RegisterPublicRoutes(chi.Router) {}
func (h *HTTPHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Get("/tenants/{tenant_id}/accounts", h.list)
	r.Post("/tenants/{tenant_id}/accounts", h.create)
	r.Post("/tenants/{tenant_id}/accounts/bulk-import", h.bulkImport)
	r.Get("/tenants/{tenant_id}/accounts/{account_id}", h.get)
	r.Patch("/tenants/{tenant_id}/accounts/{account_id}", h.update)
	r.Post("/tenants/{tenant_id}/accounts/{account_id}/credentials/rotate", h.rotate)
}

type listResponse struct {
	Items      []Account `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	_, actor, _, ok := h.actor(w, r)
	if !ok {
		return
	}
	limit, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	items, err := h.service.List(r.Context(), actor, after, afterID, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	next := ""
	if len(items) == int(limit) && len(items) > 0 {
		last := items[len(items)-1]
		next = httpserver.EncodeCursor(last.CreatedAt, last.ID)
	}
	httpserver.WriteJSON(w, http.StatusOK, listResponse{Items: items, NextCursor: next})
}
func (h *HTTPHandler) get(w http.ResponseWriter, r *http.Request) {
	_, actor, _, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeUUID(w, r, "account_id")
	if !ok {
		return
	}
	item, err := h.service.Get(r.Context(), actor, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, item)
}
func (h *HTTPHandler) create(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.actor(w, r)
	if !ok {
		return
	}
	var cmd CreateCommand
	body, err := httpserver.DecodeJSONBytes(r, &cmd)
	if err != nil {
		badBody(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, principal, tenantID, "/api/v1/tenants/{tenant_id}/accounts", body), func(ctx context.Context) (int, any, error) {
		value, err := h.service.Create(ctx, actor, cmd)
		return http.StatusCreated, value, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) update(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeUUID(w, r, "account_id")
	if !ok {
		return
	}
	var cmd UpdateCommand
	body, err := httpserver.DecodeJSONBytes(r, &cmd)
	if err != nil {
		badBody(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, principal, tenantID, "/api/v1/tenants/{tenant_id}/accounts/{account_id}", body), func(ctx context.Context) (int, any, error) {
		value, err := h.service.Update(ctx, actor, id, cmd)
		return http.StatusOK, value, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) rotate(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeUUID(w, r, "account_id")
	if !ok {
		return
	}
	var request struct {
		Credential Credential `json:"credential"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		badBody(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, principal, tenantID, "/api/v1/tenants/{tenant_id}/accounts/{account_id}/credentials/rotate", body), func(ctx context.Context) (int, any, error) {
		value, err := h.service.RotateCredential(ctx, actor, id, request.Credential)
		return http.StatusCreated, value, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) bulkImport(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.actor(w, r)
	if !ok {
		return
	}
	var request struct {
		Items []CreateCommand `json:"items"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		badBody(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, principal, tenantID, "/api/v1/tenants/{tenant_id}/accounts/bulk-import", body), func(ctx context.Context) (int, any, error) {
		value, err := h.service.BulkImport(ctx, actor, request.Items)
		return http.StatusOK, map[string]any{"items": value}, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func idem(r *http.Request, p httpserver.Principal, tenantID uuid.UUID, route string, body []byte) httpserver.IdempotencyRequest {
	return httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: route, Key: r.Header.Get("Idempotency-Key"), Body: body}
}
func (h *HTTPHandler) actor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, tenancy.TenantActor, uuid.UUID, bool) {
	p, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return p, tenancy.TenantActor{}, uuid.Nil, false
	}
	tenantID, ok := routeUUID(w, r, "tenant_id")
	if !ok {
		return p, tenancy.TenantActor{}, uuid.Nil, false
	}
	subject := tenancy.Subject{ActorID: p.ActorID}
	for _, role := range p.PlatformRoles {
		subject.PlatformRoles = append(subject.PlatformRoles, tenancy.Role(role))
	}
	for id, roles := range p.TenantRoles {
		for _, role := range roles {
			subject.TenantGrants = append(subject.TenantGrants, tenancy.TenantGrant{TenantID: id, Role: tenancy.Role(role)})
		}
	}
	requestID, _ := uuid.Parse(requestctx.RequestID(r.Context()))
	sourceIP, _ := netip.ParseAddr(requestctx.ClientIP(r.Context()))
	ua := strings.TrimSpace(r.UserAgent())
	if ua == "" {
		ua = "unknown"
	}
	actorType := strings.TrimSpace(p.ActorType)
	if actorType == "" {
		actorType = "unknown"
	}
	actor, err := tenancy.NewTenantActor(subject, tenantID, tenancy.ActorMetadata{ActorType: actorType, SourceIP: sourceIP.Unmap(), UserAgent: ua, RequestID: requestID})
	if err != nil {
		h.writeError(w, r, ErrForbidden)
		return p, tenancy.TenantActor{}, uuid.Nil, false
	}
	return p, actor, tenantID, true
}
func routeUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": name})
		return uuid.Nil, false
	}
	return id, true
}
func badBody(w http.ResponseWriter, r *http.Request) {
	httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
}
func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, httpserver.ErrIdempotencyRequired):
		httpserver.WriteError(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required", nil)
	case errors.Is(err, httpserver.ErrIdempotencyConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key conflicts with an earlier request", nil)
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, httpserver.ErrIdempotencyInvalid):
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, ErrForbidden):
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "resource was not found", nil)
	case errors.Is(err, ErrAlreadyExists):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_already_exists", "resource already exists", nil)
	case errors.Is(err, ErrVersionConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_version_conflict", "resource version does not match", nil)
	case errors.Is(err, ErrQuotaExceeded):
		httpserver.WriteError(w, r, http.StatusConflict, "quota_exceeded", "tenant account quota is exhausted", map[string]any{"quota": "accounts"})
	case errors.Is(err, ErrCapacityExhausted):
		httpserver.WriteError(w, r, http.StatusConflict, "account_capacity_exhausted", "account capacity is exhausted", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
