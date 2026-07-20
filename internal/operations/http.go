package operations

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/znicelya/ajiasu-proxy/internal/platform/requestctx"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type HTTPHandler struct{ service *Service }

func NewHTTPHandler(service *Service) (*HTTPHandler, error) {
	if service == nil {
		return nil, ErrInvalidArgument
	}
	return &HTTPHandler{service: service}, nil
}

func (h *HTTPHandler) RegisterPublicRoutes(chi.Router) {}

func (h *HTTPHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Get("/operations", h.listPlatform)
	r.Get("/operations/{operation_id}", h.getPlatform)
	r.Get("/tenants/{tenant_id}/operations", h.listTenant)
	r.Get("/tenants/{tenant_id}/operations/{operation_id}", h.getTenant)
}

func (h *HTTPHandler) listPlatform(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.platformActor(w, r)
	if !ok {
		return
	}
	limit, before, beforeID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		writeInvalidPage(w, r)
		return
	}
	items, err := h.service.ListPlatform(r.Context(), actor, before, beforeID, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeList(w, items, limit)
}

func (h *HTTPHandler) getPlatform(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.platformActor(w, r)
	if !ok {
		return
	}
	id, ok := operationRouteID(w, r)
	if !ok {
		return
	}
	item, err := h.service.GetPlatform(r.Context(), actor, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, item)
}

func (h *HTTPHandler) listTenant(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	limit, before, beforeID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		writeInvalidPage(w, r)
		return
	}
	items, err := h.service.ListTenant(r.Context(), actor, before, beforeID, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeList(w, items, limit)
}

func (h *HTTPHandler) getTenant(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	id, ok := operationRouteID(w, r)
	if !ok {
		return
	}
	item, err := h.service.GetTenant(r.Context(), actor, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, item)
}

func writeList(w http.ResponseWriter, items []Operation, limit int32) {
	next := ""
	if len(items) == int(limit) && len(items) > 0 {
		last := items[len(items)-1]
		next = httpserver.EncodeCursor(last.CreatedAt, last.ID)
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (h *HTTPHandler) platformActor(w http.ResponseWriter, r *http.Request) (tenancy.PlatformActor, bool) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return tenancy.PlatformActor{}, false
	}
	subject := subjectFromPrincipal(principal)
	actor, err := tenancy.NewPlatformActor(subject, metadataFromRequest(r, principal.ActorType))
	if err != nil || !actor.Allows(tenancy.ActionReadPlatformOps) {
		h.writeError(w, r, ErrForbidden)
		return tenancy.PlatformActor{}, false
	}
	return actor, true
}

func (h *HTTPHandler) tenantActor(w http.ResponseWriter, r *http.Request) (tenancy.TenantActor, bool) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return tenancy.TenantActor{}, false
	}
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": "tenant_id"})
		return tenancy.TenantActor{}, false
	}
	actor, err := tenancy.NewTenantActor(subjectFromPrincipal(principal), tenantID, metadataFromRequest(r, principal.ActorType))
	if err != nil {
		h.writeError(w, r, ErrForbidden)
		return tenancy.TenantActor{}, false
	}
	return actor, true
}

func subjectFromPrincipal(principal httpserver.Principal) tenancy.Subject {
	subject := tenancy.Subject{ActorID: principal.ActorID}
	for _, role := range principal.PlatformRoles {
		subject.PlatformRoles = append(subject.PlatformRoles, tenancy.Role(role))
	}
	for tenantID, roles := range principal.TenantRoles {
		for _, role := range roles {
			subject.TenantGrants = append(subject.TenantGrants, tenancy.TenantGrant{TenantID: tenantID, Role: tenancy.Role(role)})
		}
	}
	return subject
}

func metadataFromRequest(r *http.Request, actorType string) tenancy.ActorMetadata {
	requestID, _ := uuid.Parse(requestctx.RequestID(r.Context()))
	sourceIP, _ := netip.ParseAddr(requestctx.ClientIP(r.Context()))
	userAgent := strings.TrimSpace(r.UserAgent())
	if userAgent == "" {
		userAgent = "unknown"
	}
	actorType = strings.TrimSpace(actorType)
	if actorType == "" {
		actorType = "unknown"
	}
	return tenancy.ActorMetadata{ActorType: actorType, SourceIP: sourceIP.Unmap(), UserAgent: userAgent, RequestID: requestID}
}

func operationRouteID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "operation_id"))
	if err != nil || id == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": "operation_id"})
		return uuid.Nil, false
	}
	return id, true
}

func writeInvalidPage(w http.ResponseWriter, r *http.Request) {
	httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidArgument):
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, ErrForbidden):
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "resource was not found", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
