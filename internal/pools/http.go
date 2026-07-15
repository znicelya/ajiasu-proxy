package pools

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
	r.Get("/tenants/{tenant_id}/account-pools", h.list)
	r.Post("/tenants/{tenant_id}/account-pools", h.create)
	r.Get("/tenants/{tenant_id}/account-pools/{pool_id}", h.get)
	r.Patch("/tenants/{tenant_id}/account-pools/{pool_id}", h.update)
	r.Get("/tenants/{tenant_id}/account-pools/{pool_id}/members", h.listMembers)
	r.Post("/tenants/{tenant_id}/account-pools/{pool_id}/members", h.addMember)
	r.Delete("/tenants/{tenant_id}/account-pools/{pool_id}/members/{membership_id}", h.removeMember)
	r.Get("/tenants/{tenant_id}/account-pools/{pool_id}/capacity", h.capacity)
}

type poolList struct {
	Items      []Pool `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type memberList struct {
	Items      []Membership `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	_, a, _, ok := h.actor(w, r)
	if !ok {
		return
	}
	limit, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	items, err := h.service.List(r.Context(), a, after, afterID, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	next := ""
	if len(items) == int(limit) && len(items) > 0 {
		last := items[len(items)-1]
		next = httpserver.EncodeCursor(last.CreatedAt, last.ID)
	}
	httpserver.WriteJSON(w, http.StatusOK, poolList{Items: items, NextCursor: next})
}
func (h *HTTPHandler) get(w http.ResponseWriter, r *http.Request) {
	_, a, _, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeUUID(w, r, "pool_id")
	if !ok {
		return
	}
	value, err := h.service.Get(r.Context(), a, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, value)
}
func (h *HTTPHandler) create(w http.ResponseWriter, r *http.Request) {
	p, a, t, ok := h.actor(w, r)
	if !ok {
		return
	}
	var cmd CreateCommand
	body, err := httpserver.DecodeJSONBytes(r, &cmd)
	if err != nil {
		badBody(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/account-pools", body), func(ctx context.Context) (int, any, error) {
		v, err := h.service.Create(ctx, a, cmd)
		return http.StatusCreated, v, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) update(w http.ResponseWriter, r *http.Request) {
	p, a, t, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeUUID(w, r, "pool_id")
	if !ok {
		return
	}
	var cmd UpdateCommand
	body, err := httpserver.DecodeJSONBytes(r, &cmd)
	if err != nil {
		badBody(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/account-pools/{pool_id}", body), func(ctx context.Context) (int, any, error) {
		v, err := h.service.Update(ctx, a, id, cmd)
		return http.StatusOK, v, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) listMembers(w http.ResponseWriter, r *http.Request) {
	_, a, _, ok := h.actor(w, r)
	if !ok {
		return
	}
	poolID, ok := routeUUID(w, r, "pool_id")
	if !ok {
		return
	}
	limit, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	items, err := h.service.ListMemberships(r.Context(), a, poolID, after, afterID, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	next := ""
	if len(items) == int(limit) && len(items) > 0 {
		last := items[len(items)-1]
		next = httpserver.EncodeCursor(last.CreatedAt, last.ID)
	}
	httpserver.WriteJSON(w, http.StatusOK, memberList{Items: items, NextCursor: next})
}
func (h *HTTPHandler) addMember(w http.ResponseWriter, r *http.Request) {
	p, a, t, ok := h.actor(w, r)
	if !ok {
		return
	}
	poolID, ok := routeUUID(w, r, "pool_id")
	if !ok {
		return
	}
	var cmd AddMembershipCommand
	body, err := httpserver.DecodeJSONBytes(r, &cmd)
	if err != nil {
		badBody(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/account-pools/{pool_id}/members", body), func(ctx context.Context) (int, any, error) {
		v, err := h.service.AddMembership(ctx, a, poolID, cmd)
		return http.StatusCreated, v, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	p, a, t, ok := h.actor(w, r)
	if !ok {
		return
	}
	poolID, ok := routeUUID(w, r, "pool_id")
	if !ok {
		return
	}
	memberID, ok := routeUUID(w, r, "membership_id")
	if !ok {
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/account-pools/{pool_id}/members/{membership_id}", nil), func(ctx context.Context) (int, any, error) {
		err := h.service.RemoveMembership(ctx, a, poolID, memberID)
		return http.StatusNoContent, nil, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) capacity(w http.ResponseWriter, r *http.Request) {
	_, a, _, ok := h.actor(w, r)
	if !ok {
		return
	}
	poolID, ok := routeUUID(w, r, "pool_id")
	if !ok {
		return
	}
	value, err := h.service.Capacity(r.Context(), a, poolID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, value)
}
func idem(r *http.Request, p httpserver.Principal, t uuid.UUID, route string, body []byte) httpserver.IdempotencyRequest {
	return httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &t, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: route, Key: r.Header.Get("Idempotency-Key"), Body: body}
}
func (h *HTTPHandler) actor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, tenancy.TenantActor, uuid.UUID, bool) {
	p, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return p, tenancy.TenantActor{}, uuid.Nil, false
	}
	t, ok := routeUUID(w, r, "tenant_id")
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
	kind := strings.TrimSpace(p.ActorType)
	if kind == "" {
		kind = "unknown"
	}
	a, err := tenancy.NewTenantActor(subject, t, tenancy.ActorMetadata{ActorType: kind, SourceIP: sourceIP.Unmap(), UserAgent: ua, RequestID: requestID})
	if err != nil {
		h.writeError(w, r, ErrForbidden)
		return p, tenancy.TenantActor{}, uuid.Nil, false
	}
	return p, a, t, true
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
	httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
}
func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, httpserver.ErrIdempotencyRequired):
		httpserver.WriteError(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required", nil)
	case errors.Is(err, httpserver.ErrIdempotencyConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key conflicts with an earlier request", nil)
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, httpserver.ErrIdempotencyInvalid):
		badBody(w, r)
	case errors.Is(err, ErrForbidden):
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "resource was not found", nil)
	case errors.Is(err, ErrAlreadyExists):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_already_exists", "resource already exists", nil)
	case errors.Is(err, ErrVersionConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_version_conflict", "resource version does not match", nil)
	case errors.Is(err, ErrQuotaExceeded):
		httpserver.WriteError(w, r, http.StatusConflict, "quota_exceeded", "tenant pool quota is exhausted", nil)
	case errors.Is(err, ErrSelectorMismatch):
		httpserver.WriteError(w, r, http.StatusConflict, "selector_mismatch", "account does not match pool selector", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
