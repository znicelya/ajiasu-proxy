package httpserver

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type Principal struct {
	ActorID       uuid.UUID
	ActorType     string
	PlatformRoles []string
	TenantRoles   map[uuid.UUID][]string
}

func (p Principal) HasPlatformRole(role string) bool {
	for _, candidate := range p.PlatformRoles {
		if candidate == role {
			return true
		}
	}
	return false
}

func (p Principal) HasTenantRole(tenantID uuid.UUID, role string) bool {
	for _, candidate := range p.TenantRoles[tenantID] {
		if candidate == role {
			return true
		}
	}
	return false
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok && principal.ActorID != uuid.Nil
}

func RequirePrincipal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		WriteError(w, r, http.StatusUnauthorized, "authentication_required", "authentication is required", nil)
		return Principal{}, false
	}
	return principal, true
}
