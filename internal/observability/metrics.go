package observability

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	registerOnce sync.Once
	requests     *prometheus.CounterVec
	duration     *prometheus.HistogramVec
)

func Register(registry prometheus.Registerer) {
	registerOnce.Do(func() {
		requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ajiasu_http_requests_total", Help: "HTTP requests handled by the control plane."}, []string{"component", "status"})
		duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ajiasu_http_request_duration_seconds", Help: "HTTP request duration in seconds.", Buckets: prometheus.DefBuckets}, []string{"component"})
		registry.MustRegister(requests, duration)
	})
}

func Handler() http.Handler {
	Register(prometheus.DefaultRegisterer)
	return promhttp.Handler()
}

func Middleware(next http.Handler) http.Handler {
	Register(prometheus.DefaultRegisterer)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		capture := &responseWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(capture, request)
		requests.WithLabelValues("control-plane", strconv.Itoa(capture.status)).Inc()
		duration.WithLabelValues("control-plane").Observe(time.Since(started).Seconds())
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *responseWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseWriter) Write(payload []byte) (int, error) {
	return writer.ResponseWriter.Write(payload)
}
