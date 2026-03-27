package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_toolbox_http_requests_total",
			Help: "Total number of HTTP requests by method, path, and status code.",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ai_toolbox_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	AuthDeniedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "ai_toolbox_auth_denied_total",
			Help: "Total number of requests denied by group access control.",
		},
	)

	ActiveUsers = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "ai_toolbox_active_users",
			Help: "Number of unique users seen in the last 5 minutes (approximate).",
		},
	)
)

func init() {
	prometheus.MustRegister(HTTPRequestsTotal, HTTPRequestDuration, AuthDeniedTotal, ActiveUsers)
}

// Handler returns the Prometheus metrics HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// InstrumentHandler wraps an HTTP handler with request metrics.
func InstrumentHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip instrumentation for metrics and static paths
		if r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		path := normalizePath(r.URL.Path)
		start := time.Now()

		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		HTTPRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rw.statusCode)).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}

// normalizePath reduces cardinality by collapsing variable path segments.
func normalizePath(path string) string {
	switch {
	case path == "/":
		return "/"
	case strings.HasPrefix(path, "/api/metrics"):
		return "/api/metrics"
	case strings.HasPrefix(path, "/api/models"):
		return "/api/models"
	case strings.HasPrefix(path, "/api/overview"):
		return "/api/overview"
	case strings.HasPrefix(path, "/api/network-policies"):
		return "/api/network-policies"
	default:
		return path
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
