package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareUsesBoundedLabels(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/secret-tenant/accounts", nil)
	recorder := httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", recorder.Code, http.StatusNoContent)
	}
	metrics := httptest.NewRecorder()
	Handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "ajiasu_http_requests_total") {
		t.Fatal("custom request metric was not exposed")
	}
	if strings.Contains(metrics.Body.String(), "secret-tenant") {
		t.Fatal("tenant identifier leaked into metrics")
	}
}
