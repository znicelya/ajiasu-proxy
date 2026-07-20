package httpserver

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/requestctx"
)

const maxRequestBodyBytes = 1 << 20

const requestIDLength = 36

var (
	requestIDPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	requestIDFallback atomic.Uint64
)

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !validRequestID(requestID) {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(requestctx.WithRequestID(r.Context(), requestID)))
	})
}

func clientIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := r.RemoteAddr
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			clientIP = host
		}
		next.ServeHTTP(w, r.WithContext(requestctx.WithClientIP(r.Context(), clientIP)))
	})
}

func recoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recover() == nil {
					return
				}
				logger.ErrorContext(r.Context(), "panic_recovered",
					slog.String("component", "httpserver"),
					slog.String("request_id", requestctx.RequestID(r.Context())),
				)
				writeError(w, r, http.StatusInternalServerError, "internal_error", "internal server error", nil)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func accessLogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			wrapped := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			logCompletion := func() {
				logger.InfoContext(r.Context(), "request_completed",
					slog.String("component", "httpserver"),
					slog.String("request_id", requestctx.RequestID(r.Context())),
					slog.String("client_ip", requestctx.ClientIP(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", wrapped.status),
					slog.Duration("duration", time.Since(started)),
				)
			}
			defer func() {
				if panicValue := recover(); panicValue != nil {
					wrapped.status = http.StatusInternalServerError
					logCompletion()
					panic(panicValue)
				}
			}()
			next.ServeHTTP(wrapped, r)
			logCompletion()
		})
	}
}

func bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request_body", "request body could not be read", nil)
			return
		}
		if len(body) > maxRequestBodyBytes {
			writeError(w, r, http.StatusRequestEntityTooLarge, "request_body_too_large", "request body exceeds 1 MiB", nil)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func validRequestID(requestID string) bool {
	return len(requestID) == requestIDLength && requestIDPattern.MatchString(requestID)
}

func newRequestID() string {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		fallback := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), requestIDFallback.Add(1))))
		copy(value[:], fallback[:16])
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
