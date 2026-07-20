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
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/config"
	"github.com/znicelya/ajiasu-proxy/internal/platform/requestctx"
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

func TestRequestIDIsReturnedAndLogged(t *testing.T) {
	const validUUID = "123e4567-e89b-42d3-a456-426614174000"
	const validUUIDv7 = "0190a5f0-7b2c-7f31-8e2a-1d4c6b8a9f00"
	tests := []struct {
		name             string
		supplied         string
		preserveSupplied bool
		rejectedValue    string
	}{
		{name: "canonical UUID", supplied: validUUID, preserveSupplied: true},
		{name: "canonical UUIDv7", supplied: validUUIDv7, preserveSupplied: true},
		{name: "generated"},
		{name: "too long", supplied: strings.Repeat("a", 200)},
		{name: "control character", supplied: validUUID + "\nspoofed"},
		{name: "surrounding whitespace", supplied: " " + validUUID + " ", rejectedValue: validUUID},
		{name: "unsafe characters", supplied: "request/id?secret=true"},
	}
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			handler := NewRouter(Dependencies{Logger: testLogger(&logs)})
			request := httptest.NewRequest(http.MethodGet, "/livez", nil)
			if tt.supplied != "" {
				request.Header.Set("X-Request-ID", tt.supplied)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			returned := response.Header().Get("X-Request-ID")
			if returned == "" {
				t.Fatal("response request ID is empty")
			}
			if !uuidPattern.MatchString(returned) {
				t.Fatalf("returned request ID is not a canonical UUID: %q", returned)
			}
			if tt.preserveSupplied && returned != tt.supplied {
				t.Fatalf("returned ID = %q, want %q", returned, tt.supplied)
			}
			if tt.supplied != "" && !tt.preserveSupplied && returned == tt.supplied {
				t.Fatalf("invalid supplied ID was echoed: %q", returned)
			}
			if tt.rejectedValue != "" && returned == tt.rejectedValue {
				t.Fatalf("invalid supplied ID was normalized instead of replaced: %q", returned)
			}

			var accessLog map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatalf("invalid JSON log: %v: %s", err, line)
				}
				if entry["msg"] == "request_completed" {
					accessLog = entry
					break
				}
			}
			if accessLog == nil {
				t.Fatalf("request_completed log not found: %s", logs.String())
			}
			if logged, _ := accessLog["request_id"].(string); logged != returned {
				t.Fatalf("logged request ID = %q, returned ID = %q", logged, returned)
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
	const requestID = "123e4567-e89b-42d3-a456-426614174000"
	var logs bytes.Buffer
	handler := NewRouter(Dependencies{
		Logger: testLogger(&logs),
		Readiness: readinessFunc(func(context.Context) error {
			panic("sensitive panic detail")
		}),
	})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "sensitive panic detail") {
		t.Fatalf("response leaked panic: %s", response.Body.String())
	}
	if strings.Contains(logs.String(), "sensitive panic detail") {
		t.Fatalf("log contains panic value: %s", logs.String())
	}

	entries := parseJSONLogs(t, logs.String())
	panicked := filterLogs(entries, "panic_recovered")
	completed := filterLogs(entries, "request_completed")
	if len(panicked) != 1 || len(completed) != 1 {
		t.Fatalf("panic logs = %d, completed logs = %d: %s", len(panicked), len(completed), logs.String())
	}
	if panicked[0]["request_id"] != requestID || completed[0]["request_id"] != requestID {
		t.Fatalf("request IDs do not match: panicked=%v completed=%v", panicked[0]["request_id"], completed[0]["request_id"])
	}
	if completed[0]["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("completed status = %v, want 500", completed[0]["status"])
	}
}

func TestStatusWriterPreservesResponseControllerCapabilities(t *testing.T) {
	underlying := httptest.NewRecorder()
	wrapper := &statusWriter{ResponseWriter: underlying, status: http.StatusOK}

	if err := http.NewResponseController(wrapper).Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if !underlying.Flushed {
		t.Fatal("underlying response writer was not flushed")
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

func parseJSONLogs(t *testing.T, output string) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON log: %v: %s", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}

func filterLogs(entries []map[string]any, message string) []map[string]any {
	var matches []map[string]any
	for _, entry := range entries {
		if entry["msg"] == message {
			matches = append(matches, entry)
		}
	}
	return matches
}
