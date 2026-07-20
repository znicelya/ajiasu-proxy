package identity

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/znicelya/ajiasu-proxy/internal/platform/requestctx"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type HTTPHandler struct {
	sessions        *SessionService
	oidc            *OIDCService
	local           *LocalService
	services        *ServiceIdentityService
	idempotency     *httpserver.IdempotencyStore
	sessionCookie   string
	trustedOrigins  []string
	oidcBindingName string
}

type HTTPOptions struct {
	Sessions       *SessionService
	OIDC           *OIDCService
	Local          *LocalService
	Services       *ServiceIdentityService
	Idempotency    *httpserver.IdempotencyStore
	SessionCookie  string
	TrustedOrigins []string
}

func NewHTTPHandler(options HTTPOptions) (*HTTPHandler, error) {
	name := strings.TrimSpace(options.SessionCookie)
	if options.Sessions == nil || options.Services == nil || options.Idempotency == nil || name == "" {
		return nil, ErrInvalidArgument
	}
	return &HTTPHandler{
		sessions: options.Sessions, oidc: options.OIDC, local: options.Local, services: options.Services,
		idempotency: options.Idempotency, sessionCookie: name, trustedOrigins: append([]string(nil), options.TrustedOrigins...),
		oidcBindingName: name + "_oidc",
	}, nil
}

func (h *HTTPHandler) RegisterPublicRoutes(router chi.Router) {
	router.Get("/auth/oidc/login", h.beginOIDC)
	router.Get("/auth/oidc/callback", h.completeOIDC)
	router.Post("/auth/local/login", h.localLogin)
}

func (h *HTTPHandler) RegisterProtectedRoutes(router chi.Router) {
	router.Get("/auth/session", h.session)
	router.Post("/auth/logout", h.logout)
	router.Get("/service-identities", h.listServiceIdentities)
	router.Post("/service-identities", h.createServiceIdentity)
	router.Post("/service-identities/{id}/tokens", h.issueServiceToken)
	router.Delete("/service-identities/{id}/tokens/{token_id}", h.revokeServiceToken)
}

func (h *HTTPHandler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata, err := authenticationMetadata(r)
		if err != nil {
			httpserver.WriteError(w, r, http.StatusUnauthorized, "authentication_failed", "authentication failed", nil)
			return
		}
		if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
			scope, tenantID, scopeErr := serviceScopeFromRequest(r)
			if scopeErr != nil {
				httpserver.WriteError(w, r, http.StatusUnauthorized, "authentication_failed", "authentication failed", nil)
				return
			}
			principal, authErr := h.services.Authenticate(r.Context(), AuthenticateServiceTokenCommand{Token: token, Scope: scope, TenantID: tenantID, Metadata: metadata})
			if authErr != nil {
				httpserver.WriteError(w, r, http.StatusUnauthorized, "authentication_failed", "authentication failed", nil)
				return
			}
			apiPrincipal := serviceAPIPrincipal(principal)
			next.ServeHTTP(w, r.WithContext(httpserver.WithPrincipal(r.Context(), apiPrincipal)))
			return
		}
		cookie, err := r.Cookie(h.sessionCookie)
		if err != nil || cookie.Value == "" {
			httpserver.WriteError(w, r, http.StatusUnauthorized, "authentication_required", "authentication is required", nil)
			return
		}
		authenticated, err := h.sessions.AuthenticateSession(r.Context(), cookie.Value)
		if err != nil {
			httpserver.WriteError(w, r, http.StatusUnauthorized, "authentication_failed", "authentication failed", nil)
			return
		}
		if unsafeMethod(r.Method) {
			if err := h.sessions.ValidateCSRF(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token"), r.Header.Get("Origin"), h.trustedOrigins); err != nil {
				httpserver.WriteError(w, r, http.StatusForbidden, "csrf_rejected", "request origin or CSRF token is invalid", nil)
				return
			}
		}
		principal := sessionAPIPrincipal(authenticated)
		next.ServeHTTP(w, r.WithContext(httpserver.WithPrincipal(r.Context(), principal)))
	})
}

func (h *HTTPHandler) beginOIDC(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil {
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "oidc_unavailable", "OIDC login is unavailable", nil)
		return
	}
	metadata, err := authenticationMetadata(r)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return
	}
	returnPath := r.URL.Query().Get("return_path")
	if returnPath == "" {
		returnPath = "/"
	}
	result, err := h.oidc.BeginOIDC(r.Context(), BeginOIDCRequest{ReturnPath: returnPath, Metadata: metadata})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	http.SetCookie(w, result.BindingCookie)
	http.Redirect(w, r, result.AuthorizationURL, http.StatusFound)
}

func (h *HTTPHandler) completeOIDC(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil {
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "oidc_unavailable", "OIDC login is unavailable", nil)
		return
	}
	binding, err := r.Cookie(h.oidcBindingName)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "oidc_invalid_state", "OIDC state is invalid", nil)
		return
	}
	metadata, err := authenticationMetadata(r)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return
	}
	result, err := h.oidc.CompleteOIDC(r.Context(), CompleteOIDCRequest{State: r.URL.Query().Get("state"), Code: r.URL.Query().Get("code"), BindingToken: binding.Value, Metadata: metadata})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	http.SetCookie(w, result.Cookie)
	w.Header().Set("X-CSRF-Token", result.Token.CSRFToken)
	http.Redirect(w, r, result.ReturnPath, http.StatusFound)
}

func (h *HTTPHandler) localLogin(w http.ResponseWriter, r *http.Request) {
	if h.local == nil {
		httpserver.WriteError(w, r, http.StatusNotFound, "local_login_disabled", "local login is disabled", nil)
		return
	}
	var request struct {
		Identifier   string `json:"identifier"`
		Password     string `json:"password"`
		SecondFactor string `json:"second_factor"`
	}
	if err := httpserver.DecodeJSON(r, &request); err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	password := []byte(request.Password)
	defer clear(password)
	metadata, err := authenticationMetadata(r)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return
	}
	principal, err := h.local.Authenticate(r.Context(), AuthenticateLocal{Identifier: request.Identifier, Password: password, SecondFactor: request.SecondFactor, Metadata: metadata})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	session, token, cookie, err := h.sessions.CreateSession(r.Context(), principal.IdentityID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	http.SetCookie(w, cookie)
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"identity_id": principal.IdentityID, "display_name": principal.DisplayName, "session_id": session.ID,
		"csrf_token": token.CSRFToken, "idle_expires_at": session.IdleExpiresAt.UTC().Format(time.RFC3339Nano),
		"absolute_expires_at": session.AbsoluteExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (h *HTTPHandler) session(w http.ResponseWriter, r *http.Request) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"actor_id": principal.ActorID, "actor_type": principal.ActorType,
		"platform_roles": principal.PlatformRoles, "tenant_roles": principal.TenantRoles,
	})
}

func (h *HTTPHandler) logout(w http.ResponseWriter, r *http.Request) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return
	}
	cookie, err := r.Cookie(h.sessionCookie)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "session cookie is missing", nil)
		return
	}
	response, _, err := h.idempotency.Execute(r.Context(), httpserver.IdempotencyRequest{
		Scope: httpserver.IdempotencyScopePlatform, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/auth/logout", Key: r.Header.Get("Idempotency-Key"), Body: []byte{},
	}, func(ctx context.Context, _ pgx.Tx) (httpserver.StoredResponse, error) {
		if err := h.sessions.RevokeSessionAs(ctx, cookie.Value, principal.ActorID); err != nil {
			return httpserver.StoredResponse{}, err
		}
		return httpserver.StoredResponse{Status: http.StatusNoContent, Body: []byte{}}, nil
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	expired := &http.Cookie{Name: h.sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	http.SetCookie(w, expired)
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) listServiceIdentities(w http.ResponseWriter, r *http.Request) {
	principal, actor, ok := h.serviceActor(w, r, "", nil)
	if !ok {
		return
	}
	pageSize, after, afterID, err := httpserver.ParsePage(r.URL.Query())
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "pagination cursor or page size is invalid", nil)
		return
	}
	items, err := h.services.List(r.Context(), actor, after, afterID, pageSize)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response := make([]map[string]any, len(items))
	for index := range items {
		response[index] = serviceIdentityDTO(items[index])
	}
	next := ""
	if len(items) == int(pageSize) {
		last := items[len(items)-1]
		next = httpserver.EncodeCursor(last.CreatedAt, last.ID)
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": response, "next_cursor": next})
	_ = principal
}

func (h *HTTPHandler) createServiceIdentity(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Scope           ServiceScope  `json:"scope"`
		TenantID        *uuid.UUID    `json:"tenant_id"`
		Name            string        `json:"name"`
		Role            tenancy.Role  `json:"role"`
		SourceCIDR      *netip.Prefix `json:"source_cidr"`
		ValidForSeconds int64         `json:"valid_for_seconds"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	principal, actor, ok := h.serviceActor(w, r, request.Scope, request.TenantID)
	if !ok {
		return
	}
	if !sameUUID(actor.TenantID, request.TenantID) {
		httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
		return
	}
	scope, tenantID := idempotencyScope(actor)
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{
		Scope: scope, TenantID: tenantID, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/service-identities", Key: r.Header.Get("Idempotency-Key"), Body: body, ProtectResponse: true,
	}, func(ctx context.Context) (int, any, error) {
		identity, token, err := h.services.Create(ctx, actor, CreateServiceIdentityCommand{
			Scope: request.Scope, TenantID: request.TenantID, Name: request.Name, Role: request.Role,
			SourceCIDR: request.SourceCIDR, ValidFor: time.Duration(request.ValidForSeconds) * time.Second,
		})
		return http.StatusCreated, map[string]any{"service_identity": serviceIdentityDTO(identity), "token": serviceTokenCreatedDTO(token)}, err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) issueServiceToken(w http.ResponseWriter, r *http.Request) {
	identityID, ok := parseIdentityRouteUUID(w, r, "id")
	if !ok {
		return
	}
	var request struct {
		Role            tenancy.Role  `json:"role"`
		SourceCIDR      *netip.Prefix `json:"source_cidr"`
		ValidForSeconds int64         `json:"valid_for_seconds"`
	}
	body, err := httpserver.DecodeJSONBytes(r, &request)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request_body", "request body is invalid", nil)
		return
	}
	principal, actor, ok := h.serviceActor(w, r, "", nil)
	if !ok {
		return
	}
	scope, tenantID := idempotencyScope(actor)
	response, _, err := h.idempotency.ExecuteJSON(r.Context(), httpserver.IdempotencyRequest{
		Scope: scope, TenantID: tenantID, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/service-identities/{id}/tokens", Key: r.Header.Get("Idempotency-Key"), Body: body, ProtectResponse: true,
	}, func(ctx context.Context) (int, any, error) {
		token, err := h.services.IssueToken(ctx, actor, IssueServiceTokenCommand{IdentityID: identityID, Role: request.Role, SourceCIDR: request.SourceCIDR, ValidFor: time.Duration(request.ValidForSeconds) * time.Second})
		return http.StatusCreated, serviceTokenCreatedDTO(token), err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) revokeServiceToken(w http.ResponseWriter, r *http.Request) {
	identityID, ok := parseIdentityRouteUUID(w, r, "id")
	if !ok {
		return
	}
	tokenID, ok := parseIdentityRouteUUID(w, r, "token_id")
	if !ok {
		return
	}
	principal, actor, ok := h.serviceActor(w, r, "", nil)
	if !ok {
		return
	}
	scope, tenantID := idempotencyScope(actor)
	response, _, err := h.idempotency.Execute(r.Context(), httpserver.IdempotencyRequest{
		Scope: scope, TenantID: tenantID, ActorID: principal.ActorID, Method: r.Method,
		CanonicalRoute: "/api/v1/service-identities/{id}/tokens/{token_id}", Key: r.Header.Get("Idempotency-Key"), Body: []byte{},
	}, func(ctx context.Context, _ pgx.Tx) (httpserver.StoredResponse, error) {
		if err := h.services.RevokeToken(ctx, actor, identityID, tokenID); err != nil {
			return httpserver.StoredResponse{}, err
		}
		return httpserver.StoredResponse{Status: http.StatusNoContent, Body: []byte{}}, nil
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteStoredResponse(w, response)
}

func (h *HTTPHandler) serviceActor(w http.ResponseWriter, r *http.Request, requested ServiceScope, tenantHint *uuid.UUID) (httpserver.Principal, ServiceActor, bool) {
	principal, ok := httpserver.RequirePrincipal(w, r)
	if !ok {
		return httpserver.Principal{}, ServiceActor{}, false
	}
	metadata, err := authenticationMetadata(r)
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return httpserver.Principal{}, ServiceActor{}, false
	}
	if requested == "" {
		requested, _, err = serviceScopeFromRequest(r)
		if err != nil {
			httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
			return httpserver.Principal{}, ServiceActor{}, false
		}
	}
	actor := ServiceActor{ActorID: principal.ActorID, Scope: requested, Metadata: metadata}
	if requested == ServiceScopePlatform {
		if !principal.HasPlatformRole(string(tenancy.PlatformAdmin)) {
			httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
			return httpserver.Principal{}, ServiceActor{}, false
		}
		actor.Role = tenancy.PlatformAdmin
	} else {
		var tenantID uuid.UUID
		var parseErr error
		if tenantHint != nil {
			tenantID = *tenantHint
			if tenantID == uuid.Nil {
				parseErr = ErrInvalidArgument
			}
		} else {
			tenantID, parseErr = requestedTenantID(r)
		}
		if parseErr != nil || !principal.HasTenantRole(tenantID, string(tenancy.TenantAdmin)) {
			httpserver.WriteError(w, r, http.StatusForbidden, "forbidden", "operation is forbidden", nil)
			return httpserver.Principal{}, ServiceActor{}, false
		}
		actor.TenantID = &tenantID
		actor.Role = tenancy.TenantAdmin
	}
	return principal, actor, true
}

func authenticationMetadata(r *http.Request) (AuthenticationMetadata, error) {
	requestID, err := uuid.Parse(requestctx.RequestID(r.Context()))
	if err != nil {
		return AuthenticationMetadata{}, ErrInvalidArgument
	}
	sourceIP, err := netip.ParseAddr(requestctx.ClientIP(r.Context()))
	if err != nil {
		return AuthenticationMetadata{}, ErrInvalidArgument
	}
	userAgent := strings.TrimSpace(r.UserAgent())
	if userAgent == "" {
		userAgent = "unknown"
	}
	return AuthenticationMetadata{SourceIP: sourceIP.Unmap(), UserAgent: userAgent, RequestID: requestID}, nil
}

func sessionAPIPrincipal(session AuthenticatedSession) httpserver.Principal {
	principal := httpserver.Principal{ActorID: session.Session.IdentityID, ActorType: "user", TenantRoles: map[uuid.UUID][]string{}}
	if session.LocalAdmin {
		principal.ActorType = "local_admin"
		principal.PlatformRoles = []string{string(tenancy.PlatformAdmin)}
	}
	for _, grant := range session.Grants {
		principal.TenantRoles[grant.TenantID] = append([]string(nil), grant.Roles...)
	}
	return principal
}

func serviceAPIPrincipal(service ServicePrincipal) httpserver.Principal {
	principal := httpserver.Principal{ActorID: service.IdentityID, ActorType: "service_identity", TenantRoles: map[uuid.UUID][]string{}}
	if service.Scope == ServiceScopePlatform {
		principal.PlatformRoles = []string{string(service.Role)}
	} else if service.TenantID != nil {
		principal.TenantRoles[*service.TenantID] = []string{string(service.Role)}
	}
	return principal
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = parts[1]
	}
	return returnValue, returnValue != ""
}

func unsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func serviceScopeFromRequest(r *http.Request) (ServiceScope, *uuid.UUID, error) {
	if tenantID, err := requestedTenantID(r); err == nil {
		return ServiceScopeTenant, &tenantID, nil
	}
	if strings.TrimSpace(r.URL.Query().Get("tenant_id")) != "" || strings.TrimSpace(r.Header.Get("X-AJiaSu-Tenant-ID")) != "" || chi.URLParam(r, "tenant_id") != "" {
		return "", nil, ErrInvalidArgument
	}
	return ServiceScopePlatform, nil, nil
}

func requestedTenantID(r *http.Request) (uuid.UUID, error) {
	value := chi.URLParam(r, "tenant_id")
	if value == "" {
		value = r.URL.Query().Get("tenant_id")
	}
	if value == "" {
		value = r.Header.Get("X-AJiaSu-Tenant-ID")
	}
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, ErrInvalidArgument
	}
	return id, nil
}

func idempotencyScope(actor ServiceActor) (httpserver.IdempotencyScope, *uuid.UUID) {
	if actor.Scope == ServiceScopeTenant {
		return httpserver.IdempotencyScopeTenant, actor.TenantID
	}
	return httpserver.IdempotencyScopePlatform, nil
}

func sameUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func parseIdentityRouteUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_uuid", "route parameter is not a valid UUID", map[string]any{"parameter": name})
		return uuid.Nil, false
	}
	return id, true
}

func serviceIdentityDTO(identity ServiceIdentity) map[string]any {
	return map[string]any{
		"id": identity.ID, "scope": identity.Scope, "tenant_id": identity.TenantID, "name": identity.Name,
		"disabled_at": identity.DisabledAt, "version": identity.Version,
		"created_at": identity.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": identity.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func serviceTokenCreatedDTO(token ServiceTokenCreated) map[string]any {
	response := map[string]any{
		"id": token.ID, "service_identity_id": token.ServiceIdentityID, "scope": token.Scope, "tenant_id": token.TenantID,
		"prefix": token.Prefix, "role": token.Role, "expires_at": token.ExpiresAt.UTC().Format(time.RFC3339Nano),
		"created_at": token.CreatedAt.UTC().Format(time.RFC3339Nano), "token": token.Plaintext,
	}
	if token.SourceCIDR != nil {
		response["source_cidr"] = token.SourceCIDR.String()
	}
	return response
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, httpserver.ErrIdempotencyRequired):
		httpserver.WriteError(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required", nil)
	case errors.Is(err, httpserver.ErrIdempotencyConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key conflicts with an earlier request", nil)
	case errors.Is(err, ErrServiceInvalidArgument), errors.Is(err, ErrInvalidArgument), errors.Is(err, httpserver.ErrIdempotencyInvalid):
		httpserver.WriteError(w, r, http.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, ErrServiceNotFound), errors.Is(err, ErrSessionNotFound):
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "resource was not found", nil)
	case errors.Is(err, ErrServiceTokenLimit):
		httpserver.WriteError(w, r, http.StatusConflict, "service_token_limit", "service identity already has two active tokens", nil)
	case errors.Is(err, ErrServiceVersionConflict):
		httpserver.WriteError(w, r, http.StatusConflict, "resource_version_conflict", "resource version does not match", nil)
	case errors.Is(err, ErrAuthenticationFailed), errors.Is(err, ErrServiceAuthenticationFailed):
		httpserver.WriteError(w, r, http.StatusUnauthorized, "authentication_failed", "authentication failed", nil)
	case errors.Is(err, ErrCSRFRejected):
		httpserver.WriteError(w, r, http.StatusForbidden, "csrf_rejected", "request origin or CSRF token is invalid", nil)
	default:
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "a required dependency is unavailable", nil)
	}
}
