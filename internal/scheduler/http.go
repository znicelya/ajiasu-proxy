package scheduler

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
	service     *AssignmentService
	idempotency *httpserver.IdempotencyStore
}

func NewHTTPHandler(service *AssignmentService, idempotency *httpserver.IdempotencyStore) (*HTTPHandler, error) {
	if service == nil || idempotency == nil {
		return nil, ErrSchedulerInvalid
	}
	return &HTTPHandler{service: service, idempotency: idempotency}, nil
}
func (handler *HTTPHandler) RegisterPublicRoutes(chi.Router) {}
func (handler *HTTPHandler) RegisterProtectedRoutes(router chi.Router) {
	router.Get("/tenants/{tenant_id}/endpoints/{endpoint_id}/assignment", handler.get)
	router.Post("/tenants/{tenant_id}/endpoints/{endpoint_id}/assignment/reconcile", handler.reconcile)
}
func (handler *HTTPHandler) get(w http.ResponseWriter, r *http.Request) {
	_, actor, _, endpointID, ok := handler.actor(w, r)
	if !ok {
		return
	}
	assignment, err := handler.service.Get(r.Context(), actor, endpointID)
	if err != nil {
		handler.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, assignment)
}
func (handler *HTTPHandler) reconcile(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, endpointID, ok := handler.actor(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedVersion int64  `json:"expected_version"`
		ReasonCode      string `json:"reason_code"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return
	}
	response, _, err := handler.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: principal.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/assignment/reconcile", Key: r.Header.Get("Idempotency-Key"), Body: body}, func(ctx context.Context) (int, any, error) {
		assignment, err := handler.service.Reconcile(ctx, actor, ReconcileCommand{EndpointID: endpointID, ExpectedEndpointVersion: request.ExpectedVersion, ReasonCode: request.ReasonCode})
		return http.StatusAccepted, assignment, err
	})
	if err != nil {
		handler.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}
func (handler *HTTPHandler) actor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, tenancy.TenantActor, uuid.UUID, uuid.UUID, bool) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return principal, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
	}
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil || tenantID == uuid.Nil {
		return principal, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
	}
	endpointID, err := uuid.Parse(chi.URLParam(r, "endpoint_id"))
	if err != nil || endpointID == uuid.Nil {
		return principal, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
	}
	subject := tenancy.Subject{ActorID: principal.ActorID}
	for id, roles := range principal.TenantRoles {
		for _, role := range roles {
			subject.TenantGrants = append(subject.TenantGrants, tenancy.TenantGrant{TenantID: id, Role: tenancy.Role(role)})
		}
	}
	requestID, _ := uuid.Parse(requestctx.RequestID(r.Context()))
	sourceIP, _ := netip.ParseAddr(requestctx.ClientIP(r.Context()))
	userAgent := strings.TrimSpace(r.UserAgent())
	if userAgent == "" {
		userAgent = "unknown"
	}
	actor, err := tenancy.NewTenantActor(subject, tenantID, tenancy.ActorMetadata{ActorType: principal.ActorType, SourceIP: sourceIP, UserAgent: userAgent, RequestID: requestID})
	if err != nil {
		return principal, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
	}
	return principal, actor, tenantID, endpointID, true
}
func (handler *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrAssignmentForbidden):
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
	case errors.Is(err, ErrAssignmentNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "assignment was not found", nil)
	case errors.Is(err, ErrAssignmentConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_version_conflict", "resource version does not match", nil)
	case errors.Is(err, ErrPoolBindingRequired):
		httpserver.WriteError(w, r, http.StatusConflict, "pool_binding_required", "endpoint is not pool bound", nil)
	case errors.Is(err, ErrLeaseBusy):
		httpserver.WriteError(w, r, http.StatusConflict, "scheduler_busy", "assignment is being reconciled", nil)
	case errors.Is(err, ErrCoordinationDown):
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "scheduler_coordination_unavailable", "scheduler coordination is unavailable", nil)
	default:
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
	}
}
