package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/requestctx"
)

type readinessFunc func(context.Context) error

func (f readinessFunc) Check(ctx context.Context) error { return f(ctx) }

func TestLivezDoesNotCallReadiness(t *testing.T) {
	var calls atomic.Int32
	router := NewRouter(Dependencies{
		Logger: testLogger(io.Discard),
		Readiness: readinessFunc(func(context.Context) error {
			calls.Add(1)
			return errors.New("database password leaked")
		}),
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("readiness calls = %d, want 0", calls.Load())
	}
}

func TestReadyzRedactsFailureAndReportsRecovery(t *testing.T) {
	var ready atomic.Bool
	router := NewRouter(Dependencies{
		Logger: testLogger(io.Discard),
		Readiness: readinessFunc(func(context.Context) error {
			if !ready.Load() {
				return errors.New("postgres://admin:database-password@db/platform")
			}
			return nil
		}),
	})

	failed := httptest.NewRecorder()
	router.ServeHTTP(failed, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed status = %d", failed.Code)
	}
	if strings.Contains(failed.Body.String(), "database-password") {
		t.Fatalf("readiness response leaked dependency error: %s", failed.Body.String())
	}
	assertErrorEnvelope(t, failed.Body.Bytes(), "not_ready", "service is not ready", failed.Header().Get("X-Request-ID"))

	ready.Store(true)
	passed := httptest.NewRecorder()
	router.ServeHTTP(passed, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if passed.Code != http.StatusOK {
		t.Fatalf("passed status = %d, body = %s", passed.Code, passed.Body.String())
	}
}

func TestRequestIDIsReturnedAndAvailableFromContext(t *testing.T) {
	tests := []struct {
		name     string
		supplied string
	}{
		{name: "supplied", supplied: "req-123"},
		{name: "generated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var contextID string
			handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contextID = requestctx.RequestID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.supplied != "" {
				request.Header.Set("X-Request-ID", tt.supplied)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			returned := response.Header().Get("X-Request-ID")
			if returned == "" || returned != contextID {
				t.Fatalf("returned ID = %q, context ID = %q", returned, contextID)
			}
			if tt.supplied != "" && returned != tt.supplied {
				t.Fatalf("returned ID = %q, want %q", returned, tt.supplied)
			}
		})
	}
}

func TestDirectPeerIPIsAvailableWithoutTrustingForwardedHeaders(t *testing.T) {
	var clientIP string
	handler := clientIPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP = requestctx.ClientIP(r.Context())
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.10:4321"
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if clientIP != "192.0.2.10" {
		t.Fatalf("client IP = %q", clientIP)
	}
}

func TestPanicRecoveryHidesPanicAndLogsRequestID(t *testing.T) {
	var logs bytes.Buffer
	handler := requestIDMiddleware(recoveryMiddleware(testLogger(&logs))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("sensitive panic detail")
	})))
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set("X-Request-ID", "panic-request")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "sensitive panic detail") {
		t.Fatalf("response leaked panic: %s", response.Body.String())
	}
	if !strings.Contains(logs.String(), "panic-request") || !strings.Contains(logs.String(), "panic_recovered") {
		t.Fatalf("log missing safe panic correlation: %s", logs.String())
	}
	if strings.Contains(logs.String(), "sensitive panic detail") {
		t.Fatalf("log contains panic value: %s", logs.String())
	}
}

func TestBodyOverOneMiBIsRejected(t *testing.T) {
	called := false
	handler := requestIDMiddleware(bodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.Copy(io.Discard, r.Body)
	})))
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, 1<<20+1)))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if called {
		t.Fatal("downstream handler was called")
	}
	assertErrorEnvelope(t, response.Body.Bytes(), "request_body_too_large", "request body exceeds 1 MiB", response.Header().Get("X-Request-ID"))
}

func TestNewServerUsesConfiguredTimeouts(t *testing.T) {
	cfg := config.HTTP{
		Bind:              "127.0.0.1:9090",
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      4 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	server := NewServer(cfg, handler)

	if server.Addr != cfg.Bind || server.Handler == nil || server.ReadHeaderTimeout != cfg.ReadHeaderTimeout || server.ReadTimeout != cfg.ReadTimeout || server.WriteTimeout != cfg.WriteTimeout || server.IdleTimeout != cfg.IdleTimeout {
		t.Fatalf("server = %#v", server)
	}
}

func assertErrorEnvelope(t *testing.T, body []byte, code, message, requestID string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			RequestID string         `json:"request_id"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("invalid JSON error: %v: %s", err, body)
	}
	if envelope.Error.Code != code || envelope.Error.Message != message || envelope.Error.RequestID != requestID || envelope.Error.Details == nil {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func testLogger(writer io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, nil))
}
