package identity

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/platform/httpserver"
	"github.com/znicelya/ajiasu-proxy/internal/platform/keyring"
	"github.com/google/uuid"
)

func TestServiceIdentityHTTPReturnsPlaintextOnceAndListsSafely(t *testing.T) {
	db := startIdentityDatabase(t)
	service, err := NewServiceIdentityService(db.pools, audit.NewService())
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionService(db.pools, audit.NewService(), SessionCookieConfig{Name: "ajiasu_session", Path: "/", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.NewAESGCM(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store, err := httpserver.NewIdempotencyStore(db.pools, ring)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(HTTPOptions{Sessions: sessions, Services: service, Idempotency: store, SessionCookie: "ajiasu_session"})
	if err != nil {
		t.Fatal(err)
	}
	principal := httpserver.Principal{ActorID: uuid.New(), ActorType: "local_admin", PlatformRoles: []string{"platform_admin"}, TenantRoles: map[uuid.UUID][]string{}}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(httpserver.WithPrincipal(r.Context(), principal)))
		})
	}
	router := httpserver.NewRouter(httpserver.Dependencies{Modules: []httpserver.ModuleRoutes{handler}, Authenticate: authenticate})
	body := []byte(`{"scope":"platform","name":"api-bot","role":"platform_admin","source_cidr":"203.0.113.0/24","valid_for_seconds":3600}`)
	createRequest := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/service-identities", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "identity-http-test/1.0")
		request.Header.Set("Idempotency-Key", "create-api-bot")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	created := createRequest()
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var payload struct {
		Token struct {
			Plaintext string `json:"token"`
		} `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token.Plaintext == "" {
		t.Fatal("creation response omitted one-time plaintext")
	}
	var storedResponse []byte
	var protected bool
	if err := db.admin.QueryRow(t.Context(), "SELECT response_body,response_protected FROM platform.idempotency_records WHERE idempotency_key='create-api-bot'").Scan(&storedResponse, &protected); err != nil {
		t.Fatal(err)
	}
	if !protected || bytes.Contains(storedResponse, []byte(payload.Token.Plaintext)) {
		t.Fatal("idempotency record did not protect the one-time plaintext")
	}
	replayed := createRequest()
	if replayed.Code != http.StatusCreated || replayed.Body.String() != created.Body.String() {
		t.Fatalf("replay status/body=%d %q", replayed.Code, replayed.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/service-identities", nil)
	listRequest.Header.Set("User-Agent", "identity-http-test/1.0")
	listed := httptest.NewRecorder()
	router.ServeHTTP(listed, listRequest)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	if bytes.Contains(listed.Body.Bytes(), []byte(payload.Token.Plaintext)) || bytes.Contains(listed.Body.Bytes(), []byte(`"token"`)) {
		t.Fatalf("list response exposed plaintext token: %s", listed.Body.String())
	}
}
