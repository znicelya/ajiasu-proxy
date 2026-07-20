package reconciler

import (
	"context"
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
	r.Post("/nodes/{node_id}/runners/{runner_id}/force-finalize", h.forceFinalize)
}

func (h *HTTPHandler) forceFinalize(w http.ResponseWriter, r *http.Request) {
	p, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return
	}
	nodeID, ok := forceRouteID(w, r, "node_id")
	if !ok {
		return
	}
	runnerID, ok := forceRouteID(w, r, "runner_id")
	if !ok {
		return
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
	actor, err := tenancy.NewPlatformActor(subject, tenancy.ActorMetadata{ActorType: kind, SourceIP: sourceIP.Unmap(), UserAgent: ua, RequestID: requestID})
	if err != nil {
		h.writeError(w, r, ErrForbidden)
		return
	}
	var req struct {
		ReasonCategory                 string `json:"reason_category"`
		DuplicateLoginRiskAcknowledged bool   `json:"duplicate_login_risk_acknowledged"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &req)
	if err != nil || !req.DuplicateLoginRiskAcknowledged {
		h.writeError(w, r, ErrInvalidArgument)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopePlatform, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/nodes/{node_id}/runners/{runner_id}/force-finalize", Key: r.Header.Get("Idempotency-Key"), Body: body}, func(ctx context.Context) (int, any, error) {
		value, err := h.service.ForceFinalize(ctx, actor, nodeID, runnerID, req.ReasonCategory)
		return http.StatusOK, value, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func forceRouteID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": name})
		return uuid.Nil, false
	}
	return id, true
}
func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, httpserver.ErrIdempotencyRequired):
		httpserver.WriteError(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required", nil)
	case errors.Is(err, ErrInvalidArgument):
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, ErrForbidden):
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "resource was not found", nil)
	case errors.Is(err, ErrStale):
		httpserver.WriteError(w, r, http.StatusConflict, "runner_node_mismatch", "runner does not belong to the requested node", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
