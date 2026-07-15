package security_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/httpserver"
)

type failingReadiness struct{ message string }

func (f failingReadiness) Check(context.Context) error { return errors.New(f.message) }

func TestSecretCanariesNeverReachHTTPResponseOrLogs(t *testing.T) {
	canaries := []string{
		"password-canary-7f4c",
		"totp-canary-9b21",
		"recovery-canary-0dd8",
		"oidc-token-canary-6a31",
		"session-token-canary-f172",
		"ajs_AAAAAAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	}
	joined := strings.Join(canaries, "|")
	var logs bytes.Buffer
	router := httpserver.NewRouter(httpserver.Dependencies{
		Logger:    slog.New(slog.NewJSONHandler(&logs, nil)),
		Readiness: failingReadiness{message: joined},
	})
	request := httptest.NewRequest(http.MethodGet, "/readyz", strings.NewReader(joined))
	request.Header.Set("Authorization", "Bearer "+canaries[len(canaries)-1])
	request.Header.Set("Cookie", "ajiasu_session="+canaries[4])
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	combined := response.Body.String() + "\n" + logs.String()
	for _, canary := range canaries {
		if strings.Contains(combined, canary) {
			t.Fatalf("secret canary leaked: %s", canary)
		}
	}
}
