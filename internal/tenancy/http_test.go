package tenancy_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/httpserver"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
)

func TestTenantHTTPIdempotencyReplayConflictAndRequiredKey(t *testing.T) {
	db := startTenancyDatabase(t)
	service := tenancy.NewService(db.pools, audit.NewService())
	store, err := httpserver.NewIdempotencyStore(db.pools)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := tenancy.NewHTTPHandler(service, store)
	if err != nil {
		t.Fatal(err)
	}
	subject := platformAdminSubject()
	principal := httpserver.Principal{ActorID: subject.ActorID, ActorType: "local_admin", PlatformRoles: []string{"platform_admin"}, TenantRoles: map[uuid.UUID][]string{}}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(httpserver.WithPrincipal(r.Context(), principal)))
		})
	}
	router := httpserver.NewRouter(httpserver.Dependencies{Modules: []httpserver.ModuleRoutes{handler}, Authenticate: authenticate})
	adminID := createUserIdentity(t, db.admin)
	body, _ := json.Marshal(map[string]any{"slug": "api-tenant", "name": "API Tenant", "initial_admin_identity_id": adminID})

	request := func(payload []byte, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "tenancy-http-test/1.0")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	first := request(body, "create-api-tenant")
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := request(body, "create-api-tenant")
	if second.Code != http.StatusCreated || second.Body.String() != first.Body.String() {
		t.Fatalf("replay status/body=%d %q want %d %q", second.Code, second.Body.String(), first.Code, first.Body.String())
	}
	var tenantRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.tenants WHERE slug='api-tenant'").Scan(&tenantRows); err != nil {
		t.Fatal(err)
	}
	if tenantRows != 1 {
		t.Fatalf("tenant rows=%d", tenantRows)
	}

	different, _ := json.Marshal(map[string]any{"slug": "api-tenant-two", "name": "Different", "initial_admin_identity_id": adminID})
	conflict := request(different, "create-api-tenant")
	if conflict.Code != http.StatusConflict || !bytes.Contains(conflict.Body.Bytes(), []byte(`"code":"idempotency_conflict"`)) {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	missing := request(different, "")
	if missing.Code != http.StatusBadRequest || !bytes.Contains(missing.Body.Bytes(), []byte(`"code":"idempotency_key_required"`)) {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
}
