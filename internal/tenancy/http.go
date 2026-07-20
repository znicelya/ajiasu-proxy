package tenancy

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/znicelya/ajiasu-proxy/internal/platform/requestctx"
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

func (h *HTTPHandler) RegisterProtectedRoutes(router chi.Router) {
	router.Get("/tenants", h.listTenants)
	router.Post("/tenants", h.createTenant)
	router.Get("/tenants/{tenant_id}", h.getTenant)
	router.Patch("/tenants/{tenant_id}", h.updateTenant)
	router.Get("/tenants/{tenant_id}/members", h.listMembers)
	router.Post("/tenants/{tenant_id}/members", h.addMember)
	router.Delete("/tenants/{tenant_id}/members/{membership_id}", h.removeMember)
	router.Get("/tenants/{tenant_id}/role-bindings", h.listRoleBindings)
	router.Post("/tenants/{tenant_id}/role-bindings", h.grantRole)
	router.Delete("/tenants/{tenant_id}/role-bindings/{binding_id}", h.revokeRole)
	router.Get("/tenants/{tenant_id}/quota", h.getQuota)
	router.Patch("/tenants/{tenant_id}/quota", h.updateQuota)
}

func (h *HTTPHandler) getQuota(w http.ResponseWriter, r *http.Request) {
	_, actor, _, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	quota, err := h.service.GetQuota(r.Context(), actor)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, quota)
}

func (h *HTTPHandler) updateQuota(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	var request UpdateQuota
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: principal.ActorID, Method: r.Method, CanonicalRoute: "/api/v1/tenants/{tenant_id}/quota", Key: r.Header.Get("Idempotency-Key"), Body: body}, func(ctx context.Context) (int, any, error) {
		value, err := h.service.UpdateQuota(ctx, actor, request)
		return http.StatusOK, value, err
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

type tenantResponse struct {
	ID        uuid.UUID   `json:"id"`
	Slug      string      `json:"slug"`
	Name      string      `json:"name"`
	State     TenantState `json:"state"`
	Version   int64       `json:"version"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
}

type membershipResponse struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	UserID    uuid.UUID `json:"user_id"`
	Version   int64     `json:"version"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

type roleBindingResponse struct {
	ID           uuid.UUID `json:"id"`
	TenantID     uuid.UUID `json:"tenant_id"`
	MembershipID uuid.UUID `json:"membership_id"`
	Role         Role      `json:"role"`
	Version      int64     `json:"version"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
}

type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (h *HTTPHandler) listTenants(w http.ResponseWriter, r *http.Request) {
	principal, actor, ok := h.platformActor(w, r)
	if !ok {
		return
	}
	pageSize, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	items, err := h.service.ListTenants(r.Context(), actor, after, afterID, pageSize)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	response := make([]tenantResponse, len(items))
	for index := range items {
		response[index] = tenantDTO(items[index])
	}
	httpserver.WriteJSON(w, http.StatusOK, listResponse[tenantResponse]{Items: response, NextCursor: nextTenantCursor(items, pageSize)})
	_ = principal
}

func (h *HTTPHandler) getTenant(w http.ResponseWriter, r *http.Request) {
	_, actor, ok := h.platformActor(w, r)
	if !ok {
		return
	}
	tenantID, ok := parseRouteUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	tenant, err := h.service.GetTenant(r.Context(), actor, tenantID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, tenantDTO(tenant))
}

func (h *HTTPHandler) createTenant(w http.ResponseWriter, r *http.Request) {
	principal, actor, ok := h.platformActor(w, r)
	if !ok {
		return
	}
	var request struct {
		Slug                   string    `json:"slug"`
		Name                   string    `json:"name"`
		InitialAdminIdentityID uuid.UUID `json:"initial_admin_identity_id"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{
		Scope: httpserver.IdempotencyScopePlatform, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/tenants", Key: r.Header.Get("Idempotency-Key"), Body: body,
	}, func(ctx context.Context) (int, any, error) {
		tenant, err := h.service.CreateTenant(ctx, actor, CreateTenant{Slug: request.Slug, Name: request.Name, InitialAdminIdentityID: request.InitialAdminIdentityID})
		return http.StatusCreated, tenantDTO(tenant), err
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) updateTenant(w http.ResponseWriter, r *http.Request) {
	principal, actor, ok := h.platformActor(w, r)
	if !ok {
		return
	}
	tenantID, ok := parseRouteUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	var request struct {
		ExpectedVersion int64        `json:"expected_version"`
		Name            *string      `json:"name"`
		State           *TenantState `json:"state"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{
		Scope: httpserver.IdempotencyScopePlatform, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/tenants/{tenant_id}", Key: r.Header.Get("Idempotency-Key"), Body: body,
	}, func(ctx context.Context) (int, any, error) {
		tenant, err := h.service.UpdateTenant(ctx, actor, UpdateTenant{TenantID: tenantID, ExpectedVersion: request.ExpectedVersion, Name: request.Name, State: request.State})
		return http.StatusOK, tenantDTO(tenant), err
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) listMembers(w http.ResponseWriter, r *http.Request) {
	_, actor, tenantID, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	pageSize, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	items, err := h.service.ListMembers(r.Context(), actor, after, afterID, pageSize)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	response := make([]membershipResponse, len(items))
	for index := range items {
		response[index] = membershipDTO(items[index])
	}
	httpserver.WriteJSON(w, http.StatusOK, listResponse[membershipResponse]{Items: response, NextCursor: nextMembershipCursor(items, pageSize)})
	_ = tenantID
}

func (h *HTTPHandler) addMember(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	var request struct {
		UserID uuid.UUID `json:"user_id"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{
		Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/tenants/{tenant_id}/members", Key: r.Header.Get("Idempotency-Key"), Body: body,
	}, func(ctx context.Context) (int, any, error) {
		membership, err := h.service.AddMember(ctx, actor, AddMember{TenantID: tenantID, UserID: request.UserID})
		return http.StatusCreated, membershipDTO(membership), err
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	membershipID, ok := parseRouteUUID(w, r, "membership_id")
	if !ok {
		return
	}
	h.executeDelete(w, r, principal.ActorID, tenantID, "/api/v1/tenants/{tenant_id}/members/{membership_id}", func(ctx context.Context) error {
		return h.service.RemoveMember(ctx, actor, membershipID)
	})
}

func (h *HTTPHandler) listRoleBindings(w http.ResponseWriter, r *http.Request) {
	_, actor, _, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	pageSize, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	items, err := h.service.ListRoleBindings(r.Context(), actor, after, afterID, pageSize)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	response := make([]roleBindingResponse, len(items))
	for index := range items {
		response[index] = roleBindingDTO(items[index])
	}
	httpserver.WriteJSON(w, http.StatusOK, listResponse[roleBindingResponse]{Items: response, NextCursor: nextRoleCursor(items, pageSize)})
}

func (h *HTTPHandler) grantRole(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	var request struct {
		MembershipID uuid.UUID `json:"membership_id"`
		Role         Role      `json:"role"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{
		Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/tenants/{tenant_id}/role-bindings", Key: r.Header.Get("Idempotency-Key"), Body: body,
	}, func(ctx context.Context) (int, any, error) {
		binding, err := h.service.GrantRole(ctx, actor, GrantRole{TenantID: tenantID, MembershipID: request.MembershipID, Role: request.Role})
		return http.StatusCreated, roleBindingDTO(binding), err
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) revokeRole(w http.ResponseWriter, r *http.Request) {
	principal, actor, tenantID, ok := h.tenantActor(w, r)
	if !ok {
		return
	}
	bindingID, ok := parseRouteUUID(w, r, "binding_id")
	if !ok {
		return
	}
	h.executeDelete(w, r, principal.ActorID, tenantID, "/api/v1/tenants/{tenant_id}/role-bindings/{binding_id}", func(ctx context.Context) error {
		return h.service.RevokeRole(ctx, actor, bindingID)
	})
}

func (h *HTTPHandler) executeDelete(w http.ResponseWriter, r *http.Request, actorID, tenantID uuid.UUID, route string, operation func(context.Context) error) {
	response, _, err := h.idempotency.Execute(r.Context(), httpserver.IdempotencyRequest{
		Scope: httpserver.IdempotencyScopeTenant, TenantID: &tenantID, ActorID: actorID, Method: r.Method,
		CanonicalRoute: route, Key: r.Header.Get("Idempotency-Key"), Body: []byte{},
	}, func(ctx context.Context, _ pgx.Tx) (httpserver.StoredResponse, error) {
		if err := operation(ctx); err != nil {
			return httpserver.StoredResponse{}, err
		}
		return httpserver.StoredResponse{Status: http.StatusNoContent, Body: []byte{}}, nil
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) platformActor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, PlatformActor, bool) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return httpserver.Principal{}, PlatformActor{}, false
	}
	actor, err := NewPlatformActor(subjectFromPrincipal(principal), metadataFromRequest(principal, r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return httpserver.Principal{}, PlatformActor{}, false
	}
	return principal, actor, true
}

func (h *HTTPHandler) tenantActor(w http.ResponseWriter, r *http.Request) (httpserver.Principal, TenantActor, uuid.UUID, bool) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return httpserver.Principal{}, TenantActor{}, uuid.Nil, false
	}
	tenantID, ok := parseRouteUUID(w, r, "tenant_id")
	if !ok {
		return httpserver.Principal{}, TenantActor{}, uuid.Nil, false
	}
	actor, err := NewTenantActor(subjectFromPrincipal(principal), tenantID, metadataFromRequest(principal, r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return httpserver.Principal{}, TenantActor{}, uuid.Nil, false
	}
	return principal, actor, tenantID, true
}

func subjectFromPrincipal(principal httpserver.Principal) Subject {
	subject := Subject{ActorID: principal.ActorID}
	for _, role := range principal.PlatformRoles {
		subject.PlatformRoles = append(subject.PlatformRoles, Role(role))
	}
	for tenantID, roles := range principal.TenantRoles {
		for _, role := range roles {
			subject.TenantGrants = append(subject.TenantGrants, TenantGrant{TenantID: tenantID, Role: Role(role)})
		}
	}
	return subject
}

func metadataFromRequest(principal httpserver.Principal, r *http.Request) ActorMetadata {
	requestID, _ := uuid.Parse(requestctx.RequestID(r.Context()))
	sourceIP, _ := netip.ParseAddr(requestctx.ClientIP(r.Context()))
	userAgent := strings.TrimSpace(r.UserAgent())
	if userAgent == "" {
		userAgent = "unknown"
	}
	actorType := strings.TrimSpace(principal.ActorType)
	if actorType == "" {
		actorType = "unknown"
	}
	return ActorMetadata{ActorType: actorType, SourceIP: sourceIP.Unmap(), UserAgent: userAgent, RequestID: requestID}
}

func parseRouteUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": name})
		return uuid.Nil, false
	}
	return id, true
}

func (h *HTTPHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
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
	case errors.Is(err, ErrVersionConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_version_conflict", "resource version does not match", nil)
	case errors.Is(err, ErrAlreadyExists):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_already_exists", "resource already exists", nil)
	case errors.Is(err, ErrTenantSuspended):
		httpserver.WriteError(w, r, http.StatusConflict, "tenant_suspended", "tenant does not accept operations", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}

func tenantDTO(value Tenant) tenantResponse {
	return tenantResponse{ID: value.ID, Slug: value.Slug, Name: value.Name, State: value.State, Version: value.Version, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}
}

func membershipDTO(value Membership) membershipResponse {
	return membershipResponse{ID: value.ID, TenantID: value.TenantID, UserID: value.UserID, Version: value.Version, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}
}

func roleBindingDTO(value RoleBinding) roleBindingResponse {
	return roleBindingResponse{ID: value.ID, TenantID: value.TenantID, MembershipID: value.MembershipID, Role: value.Role, Version: value.Version, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func nextTenantCursor(items []Tenant, pageSize int32) string {
	if len(items) != int(pageSize) {
		return ""
	}
	last := items[len(items)-1]
	return httpserver.EncodeCursor(last.CreatedAt, last.ID)
}

func nextMembershipCursor(items []Membership, pageSize int32) string {
	if len(items) != int(pageSize) {
		return ""
	}
	last := items[len(items)-1]
	return httpserver.EncodeCursor(last.CreatedAt, last.ID)
}

func nextRoleCursor(items []RoleBinding, pageSize int32) string {
	if len(items) != int(pageSize) {
		return ""
	}
	last := items[len(items)-1]
	return httpserver.EncodeCursor(last.CreatedAt, last.ID)
}
