package proxyaccess

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/znicelya/ajiasu-proxy/internal/platform/requestctx"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	r.Get("/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials", h.listCredentials)
	r.Post("/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials", h.createCredential)
	r.Post("/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials/{credential_id}/rotate", h.rotateCredential)
	r.Delete("/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials/{credential_id}", h.revokeCredential)
	r.Get("/tenants/{tenant_id}/endpoints/{endpoint_id}/access-profile", h.getProfile)
	r.Patch("/tenants/{tenant_id}/endpoints/{endpoint_id}/access-profile", h.putProfile)
}

func (h *HTTPHandler) listCredentials(w http.ResponseWriter, r *http.Request) {
	p, actor, tenantID, endpointID, ok := h.actor(w, r)
	if !ok {
		return
	}
	items, err := h.service.listCredentials(r.Context(), actor, endpointID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	_ = p
	_ = tenantID
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *HTTPHandler) createCredential(w http.ResponseWriter, r *http.Request) {
	p, actor, tenantID, endpointID, ok := h.actor(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		badProxyRequest(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials", Key: r.Header.Get("Idempotency-Key"), Body: body, ProtectResponse: true}, func(ctx context.Context) (int, any, error) {
		created, err := h.service.CreateCredential(ctx, actor, CreateCredentialCommand{EndpointID: endpointID, ExpiresAt: request.ExpiresAt})
		return http.StatusCreated, created, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) rotateCredential(w http.ResponseWriter, r *http.Request) {
	p, actor, tenantID, endpointID, ok := h.actor(w, r)
	if !ok {
		return
	}
	credentialID, ok := routeUUID(w, r, "credential_id")
	if !ok {
		return
	}
	var request struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		badProxyRequest(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials/{credential_id}/rotate", Key: r.Header.Get("Idempotency-Key"), Body: body, ProtectResponse: true}, func(ctx context.Context) (int, any, error) {
		created, err := h.service.RotateCredential(ctx, actor, RotateCredentialCommand{CredentialID: credentialID, ExpiresAt: request.ExpiresAt})
		return http.StatusCreated, created, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	_ = endpointID
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) revokeCredential(w http.ResponseWriter, r *http.Request) {
	p, actor, tenantID, endpointID, ok := h.actor(w, r)
	if !ok {
		return
	}
	credentialID, ok := routeUUID(w, r, "credential_id")
	if !ok {
		return
	}
	response, _, err := h.idempotency.Execute(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/proxy-credentials/{credential_id}", Key: r.Header.Get("Idempotency-Key"), Body: []byte{}}, func(ctx context.Context, _ pgx.Tx) (httpserver.StoredResponse, error) {
		return httpserver.StoredResponse{Status: http.StatusNoContent}, h.service.RevokeCredential(ctx, actor, credentialID)
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	_ = endpointID
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) getProfile(w http.ResponseWriter, r *http.Request) {
	_, actor, _, endpointID, ok := h.actor(w, r)
	if !ok {
		return
	}
	profile, err := h.service.GetAccessProfile(r.Context(), actor, endpointID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, profile)
}

func (h *HTTPHandler) putProfile(w http.ResponseWriter, r *http.Request) {
	p, actor, tenantID, endpointID, ok := h.actor(w, r)
	if !ok {
		return
	}
	var request struct {
		Policy          Policy `json:"policy"`
		ExpectedVersion *int64 `json:"expected_version,omitempty"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		badProxyRequest(w, r)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: p.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/tenants/{tenant_id}/endpoints/{endpoint_id}/access-profile", Key: r.Header.Get("Idempotency-Key"), Body: body}, func(ctx context.Context) (int, any, error) {
		profile, err := h.service.UpsertAccessProfile(ctx, actor, UpsertAccessProfileCommand{EndpointID: endpointID, Policy: request.Policy, ExpectedVersion: request.ExpectedVersion})
		return http.StatusOK, profile, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (s *Service) listCredentials(ctx context.Context, actor tenancy.TenantActor, endpointID uuid.UUID) ([]ProxyCredential, error) {
	if !actor.Allows(tenancy.ActionReadResources) || endpointID == uuid.Nil {
		return nil, ErrForbidden
	}
	return database.InTenantTx(ctx, s.pools.Tenant, actor.TenantID(), actor.ActorID(), func(ctx context.Context, tx pgx.Tx) ([]ProxyCredential, error) {
		rows, err := tx.Query(ctx, `SELECT id,tenant_id,endpoint_id,public_identifier,expires_at,revoked_at,created_at,updated_at FROM endpoints.proxy_credentials WHERE tenant_id=$1 AND endpoint_id=$2 ORDER BY created_at,id`, actor.TenantID(), endpointID)
		if err != nil {
			return nil, mapStorageError(err)
		}
		defer rows.Close()
		var items []ProxyCredential
		for rows.Next() {
			var item ProxyCredential
			if err := rows.Scan(&item.ID, &item.TenantID, &item.EndpointID, &item.PublicIdentifier, &item.ExpiresAt, &item.RevokedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
				return nil, ErrStorage
			}
			items = append(items, item)
		}
		return items, rows.Err()
	})
}

func (h *HTTPHandler) actor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, tenancy.TenantActor, uuid.UUID, uuid.UUID, bool) {
	p, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return p, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
	}
	tenantID, ok := routeUUID(w, r, "tenant_id")
	if !ok {
		return p, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
	}
	endpointID, ok := routeUUID(w, r, "endpoint_id")
	if !ok {
		return p, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
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
	actorType := strings.TrimSpace(p.ActorType)
	if actorType == "" {
		actorType = "unknown"
	}
	actor, err := tenancy.NewTenantActor(subject, tenantID, tenancy.ActorMetadata{ActorType: actorType, SourceIP: sourceIP.Unmap(), UserAgent: r.UserAgent(), RequestID: requestID})
	if err != nil {
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
		return p, tenancy.TenantActor{}, uuid.Nil, uuid.Nil, false
	}
	return p, actor, tenantID, endpointID, true
}
func routeUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	value, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || value == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": name})
		return uuid.Nil, false
	}
	return value, true
}
func badProxyRequest(w http.ResponseWriter, r *http.Request) {
	httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
}
func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, httpserver.ErrIdempotencyRequired):
		httpserver.WriteError(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required", nil)
	case errors.Is(err, ErrInvalidArgument):
		badProxyRequest(w, r)
	case errors.Is(err, ErrForbidden):
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "resource was not found", nil)
	case errors.Is(err, ErrVersionConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_version_conflict", "resource version does not match", nil)
	case errors.Is(err, ErrPoolBinding):
		httpserver.WriteError(w, r, http.StatusConflict, "pool_binding_not_supported", "pool binding is not supported", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
