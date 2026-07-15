package endpoints

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
	r.Get("/tenants/{tenant_id}/endpoints", h.list)
	r.Post("/tenants/{tenant_id}/endpoints", h.create)
	r.Get("/tenants/{tenant_id}/endpoints/{endpoint_id}", h.get)
	r.Patch("/tenants/{tenant_id}/endpoints/{endpoint_id}", h.patch)
	r.Delete("/tenants/{tenant_id}/endpoints/{endpoint_id}", h.delete)
	r.Post("/tenants/{tenant_id}/endpoints/{endpoint_id}/start", h.start)
	r.Post("/tenants/{tenant_id}/endpoints/{endpoint_id}/stop", h.stop)
	r.Post("/tenants/{tenant_id}/endpoints/{endpoint_id}/rebuild", h.rebuild)
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
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}
func (h *HTTPHandler) get(w http.ResponseWriter, r *http.Request) {
	_, a, _, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "endpoint_id")
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
	var req struct {
		Name               string             `json:"name"`
		AccountID          uuid.UUID          `json:"account_id"`
		NodeID             uuid.UUID          `json:"node_id"`
		DesiredRunnerState DesiredRunnerState `json:"desired_runner_state"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &req)
	if err != nil {
		bad(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/endpoints", body), func(ctx context.Context) (int, any, error) {
		endpoint, operation, err := h.service.Create(ctx, a, CreateCommand{Name: req.Name, AccountID: req.AccountID, NodeID: req.NodeID, DesiredRunnerState: req.DesiredRunnerState})
		return http.StatusAccepted, map[string]any{"endpoint": endpoint, "operation": operation}, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) delete(w http.ResponseWriter, r *http.Request) {
	p, a, t, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "endpoint_id")
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
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}", body), func(ctx context.Context) (int, any, error) {
		endpoint, operation, err := h.service.RequestDelete(ctx, a, id, req.ExpectedVersion)
		return http.StatusAccepted, map[string]any{"endpoint": endpoint, "operation": operation}, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) patch(w http.ResponseWriter, r *http.Request) {
	p, a, t, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "endpoint_id")
	if !ok {
		return
	}
	var req struct {
		ExpectedVersion    int64               `json:"expected_version"`
		DesiredRunnerState *DesiredRunnerState `json:"desired_runner_state"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &req)
	if err != nil || req.DesiredRunnerState == nil || (*req.DesiredRunnerState != DesiredRunning && *req.DesiredRunnerState != DesiredStopped) {
		bad(w, r)
		return
	}
	action := "start"
	if *req.DesiredRunnerState == DesiredStopped {
		action = "stop"
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}", body), func(ctx context.Context) (int, any, error) {
		endpoint, operation, err := h.service.RequestState(ctx, a, id, req.ExpectedVersion, action)
		return http.StatusAccepted, map[string]any{"endpoint": endpoint, "operation": operation}, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) action(w http.ResponseWriter, r *http.Request, action string) {
	p, a, t, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := routeID(w, r, "endpoint_id")
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
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), idem(r, p, t, "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/"+action, body), func(ctx context.Context) (int, any, error) {
		endpoint, operation, err := h.service.RequestState(ctx, a, id, req.ExpectedVersion, action)
		return http.StatusAccepted, map[string]any{"endpoint": endpoint, "operation": operation}, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (h *HTTPHandler) start(w http.ResponseWriter, r *http.Request)   { h.action(w, r, "start") }
func (h *HTTPHandler) stop(w http.ResponseWriter, r *http.Request)    { h.action(w, r, "stop") }
func (h *HTTPHandler) rebuild(w http.ResponseWriter, r *http.Request) { h.action(w, r, "rebuild") }
func idem(r *http.Request, p httpserver.Principal, t uuid.UUID, route string, body []byte) httpserver.IdempotencyRequest {
	return httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &t, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: route, Key: r.Header.Get("Idempotency-Key"), Body: body}
}
func (h *HTTPHandler) actor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, tenancy.TenantActor, uuid.UUID, bool) {
	p, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return p, tenancy.TenantActor{}, uuid.Nil, false
	}
	t, ok := routeID(w, r, "tenant_id")
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
	case errors.Is(err, ErrNodeUnavailable):
		httpserver.WriteError(w, r, http.StatusConflict, "node_unavailable", "node does not accept runners", nil)
	case errors.Is(err, ErrNodeCapacity):
		httpserver.WriteError(w, r, http.StatusConflict, "node_capacity_exhausted", "node runner capacity is exhausted", nil)
	case errors.Is(err, ErrAccountCapacity):
		httpserver.WriteError(w, r, http.StatusConflict, "account_capacity_exhausted", "account capacity is exhausted", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
