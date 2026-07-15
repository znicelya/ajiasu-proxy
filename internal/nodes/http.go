package nodes

import (
	"context"
	"errors"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/httpserver"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/requestctx"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"net/netip"
	"strings"
	"time"
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
	r.Post("/node-enrollments", h.createEnrollment)
	r.Delete("/node-enrollments/{enrollment_id}", h.revokeEnrollment)
	r.Get("/nodes", h.list)
	r.Get("/nodes/{node_id}", h.get)
	r.Patch("/nodes/{node_id}", h.patch)
	r.Post("/nodes/{node_id}/drain", h.drain)
	r.Get("/tenants/{tenant_id}/runner-nodes", h.listEligible)
}
func (h *HTTPHandler) listEligible(w http.ResponseWriter, r *http.Request) {
	p, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return
	}
	tenantID, ok := routeID(w, r, "tenant_id")
	if !ok {
		return
	}
	subject := tenancy.Subject{ActorID: p.ActorID}
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
	a, err := tenancy.NewTenantActor(subject, tenantID, tenancy.ActorMetadata{ActorType: kind, SourceIP: sourceIP.Unmap(), UserAgent: ua, RequestID: requestID})
	if err != nil {
		h.writeError(w, r, ErrForbidden)
		return
	}
	items, err := h.service.ListEligible(r.Context(), a)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (h *HTTPHandler) drain(w http.ResponseWriter, r *http.Request) {
	p, a, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "node_id")
	if !ok {
		return
	}
	var req struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &req)
	if err != nil {
		bad(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopePlatform, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/nodes/{node_id}/drain", Key: r.Header.Get("Idempotency-Key"), Body: body}, func(ctx context.Context) (int, any, error) {
		value, err := h.service.SetMaintenance(ctx, a, id, req.ExpectedVersion, MaintenanceDraining)
		return http.StatusOK, value, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) revokeEnrollment(w http.ResponseWriter, r *http.Request) {
	p, a, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "enrollment_id")
	if !ok {
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopePlatform, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/node-enrollments/{enrollment_id}", Key: r.Header.Get("Idempotency-Key"), Body: nil}, func(ctx context.Context) (int, any, error) {
		err := h.service.RevokeEnrollment(ctx, a, id)
		return http.StatusNoContent, nil, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) createEnrollment(w http.ResponseWriter, r *http.Request) {
	p, a, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedNodeName string `json:"expected_node_name"`
		ValidForSeconds  int64  `json:"valid_for_seconds"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &req)
	if err != nil {
		bad(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopePlatform, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/node-enrollments", Key: r.Header.Get("Idempotency-Key"), Body: body}, func(ctx context.Context) (int, any, error) {
		value, err := h.service.CreateEnrollment(ctx, a, CreateEnrollment{ExpectedNodeName: req.ExpectedNodeName, ValidFor: time.Duration(req.ValidForSeconds) * time.Second})
		return http.StatusCreated, value, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	_, a, ok := h.actor(w, r)
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
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}
func (h *HTTPHandler) get(w http.ResponseWriter, r *http.Request) {
	_, a, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "node_id")
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
func (h *HTTPHandler) patch(w http.ResponseWriter, r *http.Request) {
	p, a, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "node_id")
	if !ok {
		return
	}
	var req struct {
		ExpectedVersion  int64            `json:"expected_version"`
		MaintenanceState MaintenanceState `json:"maintenance_state"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &req)
	if err != nil {
		bad(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopePlatform, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/nodes/{node_id}", Key: r.Header.Get("Idempotency-Key"), Body: body}, func(ctx context.Context) (int, any, error) {
		value, err := h.service.SetMaintenance(ctx, a, id, req.ExpectedVersion, req.MaintenanceState)
		return http.StatusOK, value, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) actor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, tenancy.PlatformActor, bool) {
	p, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return p, tenancy.PlatformActor{}, false
	}
	subject := tenancy.Subject{ActorID: p.ActorID}
	for _, role := range p.PlatformRoles {
		subject.PlatformRoles = append(subject.PlatformRoles, tenancy.Role(role))
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
	a, err := tenancy.NewPlatformActor(subject, tenancy.ActorMetadata{ActorType: kind, SourceIP: sourceIP.Unmap(), UserAgent: ua, RequestID: requestID})
	if err != nil {
		h.writeError(w, r, ErrForbidden)
		return p, tenancy.PlatformActor{}, false
	}
	return p, a, true
}
func routeID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": name})
		return uuid.Nil, false
	}
	return id, true
}
func bad(w http.ResponseWriter, r *http.Request) {
	httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
}
func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, httpserver.ErrIdempotencyRequired):
		httpserver.WriteError(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required", nil)
	case errors.Is(err, ErrInvalidArgument):
		bad(w, r)
	case errors.Is(err, ErrForbidden):
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "resource was not found", nil)
	case errors.Is(err, ErrAlreadyExists):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_already_exists", "resource already exists", nil)
	case errors.Is(err, ErrVersionConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_version_conflict", "resource version does not match", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
