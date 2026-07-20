package audit

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type HTTPHandler struct{ reader *Reader }

func NewHTTPHandler(reader *Reader) (*HTTPHandler, error) {
	if reader == nil {
		return nil, ErrReadInvalid
	}
	return &HTTPHandler{reader: reader}, nil
}

func (h *HTTPHandler) RegisterPublicRoutes(chi.Router) {}

func (h *HTTPHandler) RegisterProtectedRoutes(router chi.Router) {
	router.Get("/audit-events", h.list)
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return
	}
	pageSize, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	request := ReadRequest{ActorID: principal.ActorID, Platform: principal.HasPlatformRole("platform_admin"), After: after, AfterID: afterID, PageSize: pageSize}
	if value := strings.TrimSpace(r.URL.Query().Get("tenant_id")); value != "" {
		tenantID, parseErr := uuid.Parse(value)
		if parseErr != nil || tenantID == uuid.Nil {
			httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "tenant_id is not a valid UUID", nil)
			return
		}
		request.TenantID = &tenantID
	}
	if !request.Platform {
		if request.TenantID == nil || !principal.HasTenantRole(*request.TenantID, "auditor") && !principal.HasTenantRole(*request.TenantID, "tenant_admin") {
			httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
			return
		}
	}
	items, err := h.reader.List(r.Context(), request)
	if err != nil {
		if errors.Is(err, ErrReadInvalid) {
			httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
		} else {
			httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
		}
		return
	}
	response := make([]map[string]any, len(items))
	for index := range items {
		item := items[index]
		response[index] = map[string]any{
			"id": item.ID, "tenant_id": item.TenantID, "actor_type": item.ActorType, "actor_id": item.ActorID,
			"action": item.Action, "resource_type": item.ResourceType, "resource_id": item.ResourceID,
			"result": item.Result, "source_ip": item.SourceIP.String(), "user_agent": item.UserAgent,
			"request_id": item.RequestID, "created_at": item.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	next := ""
	if len(items) == int(pageSize) {
		last := items[len(items)-1]
		next = httpserver.EncodeCursor(last.CreatedAt, last.ID)
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": response, "next_cursor": next})
}
