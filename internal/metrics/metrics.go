// Package metrics provides Prometheus instrumentation for vibeD.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all vibeD Prometheus metrics.
type Metrics struct {
	// Build metrics
	BuildsTotal    *prometheus.CounterVec
	BuildDuration  *prometheus.HistogramVec
	BuildsInFlight prometheus.Gauge

	// Deploy metrics
	DeploysTotal   *prometheus.CounterVec
	DeployDuration *prometheus.HistogramVec

	// Artifact metrics
	ArtifactsActive *prometheus.GaugeVec
	DeletesTotal    *prometheus.CounterVec

	// MCP tool metrics
	ToolCallsTotal    *prometheus.CounterVec
	ToolCallDuration  *prometheus.HistogramVec

	// HTTP API metrics
	HTTPRequestsTotal    *prometheus.CounterVec
	HTTPRequestDuration  *prometheus.HistogramVec

	// GC metrics
	GCResourcesCleaned *prometheus.CounterVec

	// SSE metrics
	SSEConnectionsActive prometheus.Gauge

	// Rate limiting metrics
	RateLimitedTotal *prometheus.CounterVec
}

// New creates and registers all vibeD metrics.
func New() *Metrics {
	return &Metrics{
		BuildsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vibed",
			Name:      "builds_total",
			Help:      "Total number of container image builds.",
		}, []string{"status", "language"}),

		BuildDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "vibed",
			Name:      "build_duration_seconds",
			Help:      "Duration of container image builds in seconds.",
			Buckets:   []float64{5, 10, 30, 60, 120, 300, 600},
		}, []string{"status", "language"}),

		BuildsInFlight: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: "vibed",
			Name:      "builds_in_flight",
			Help:      "Number of builds currently in progress.",
		}),

		DeploysTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vibed",
			Name:      "deploys_total",
			Help:      "Total number of deployments.",
		}, []string{"status", "target"}),

		DeployDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "vibed",
			Name:      "deploy_duration_seconds",
			Help:      "Duration of deployments in seconds.",
			Buckets:   []float64{1, 2, 5, 10, 30, 60},
		}, []string{"status", "target"}),

		ArtifactsActive: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "vibed",
			Name:      "artifacts_active",
			Help:      "Number of currently active artifacts by target.",
		}, []string{"target"}),

		DeletesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vibed",
			Name:      "deletes_total",
			Help:      "Total number of artifact deletions.",
		}, []string{"status"}),

		ToolCallsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vibed",
			Name:      "mcp_tool_calls_total",
			Help:      "Total number of MCP tool invocations.",
		}, []string{"tool", "status"}),

		ToolCallDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "vibed",
			Name:      "mcp_tool_call_duration_seconds",
			Help:      "Duration of MCP tool calls in seconds.",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 120, 300},
		}, []string{"tool"}),

		HTTPRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vibed",
			Name:      "http_requests_total",
			Help:      "Total number of HTTP API requests.",
		}, []string{"method", "path", "status_code"}),

		HTTPRequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "vibed",
			Name:      "http_request_duration_seconds",
			Help:      "Duration of HTTP API requests in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "path"}),

		GCResourcesCleaned: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vibed",
			Name:      "gc_resources_cleaned_total",
			Help:      "Total resources cleaned by garbage collector.",
		}, []string{"type"}),

		SSEConnectionsActive: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: "vibed",
			Name:      "sse_connections_active",
			Help:      "Number of active SSE connections.",
		}),

		RateLimitedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vibed",
			Subsystem: "http",
			Name:      "rate_limited_total",
			Help:      "Total number of rate-limited HTTP requests.",
		}, []string{"client_type"}),
	}
}
