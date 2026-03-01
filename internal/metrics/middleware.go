package metrics

import (
	"fmt"
	"net/http"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// HTTPMiddleware returns an HTTP middleware that records request metrics.
func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		path := normalizePath(r.URL.Path)

		m.HTTPRequestsTotal.WithLabelValues(r.Method, path, fmt.Sprintf("%d", rw.statusCode)).Inc()
		m.HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}

// normalizePath groups dynamic path segments to avoid high-cardinality labels.
func normalizePath(path string) string {
	switch {
	case path == "/healthz" || path == "/readyz" || path == "/metrics":
		return path
	case path == "/api/targets":
		return "/api/targets"
	case path == "/api/artifacts":
		return "/api/artifacts"
	case len(path) > len("/api/artifacts/") && path[:len("/api/artifacts/")] == "/api/artifacts/":
		// Normalize /api/artifacts/{id} and /api/artifacts/{id}/logs
		if len(path) > 5 && path[len(path)-5:] == "/logs" {
			return "/api/artifacts/:id/logs"
		}
		return "/api/artifacts/:id"
	case len(path) > len("/mcp") && path[:len("/mcp")] == "/mcp":
		return "/mcp"
	default:
		return "/static"
	}
}
