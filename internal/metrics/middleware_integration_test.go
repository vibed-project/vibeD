//go:build integration

package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vibed-project/vibeD/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPMiddleware_RecordsRequests(t *testing.T) {
	m := metrics.New()

	handler := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/artifacts" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Make some requests
	func() {
	        resp, err := http.Get(srv.URL + "/api/artifacts")
	        require.NoError(t, err)
	        defer resp.Body.Close()
	        assert.Equal(t, http.StatusOK, resp.StatusCode)
	}()

	func() {
	        resp, err := http.Get(srv.URL + "/api/artifacts/abc123")
	        require.NoError(t, err)
	        defer resp.Body.Close()
	}()

	func() {
	        resp, err := http.Get(srv.URL + "/healthz")
	        require.NoError(t, err)
	        defer resp.Body.Close()
	}()
	// Gather all metrics and check
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	var requestsTotal *io_prometheus_client.MetricFamily
	for _, fam := range families {
		if fam.GetName() == "vibed_http_requests_total" {
			requestsTotal = fam
			break
		}
	}

	require.NotNil(t, requestsTotal, "vibed_http_requests_total should exist")
	assert.GreaterOrEqual(t, len(requestsTotal.GetMetric()), 1, "should have at least one metric entry")
}

func TestNormalizePath(t *testing.T) {
	// normalizePath keeps label cardinality bounded: unknown paths (including
	// former /api/artifacts/{id} routes now that the orchestrator is gone)
	// collapse to a single "/static" bucket, while /mcp sub-paths collapse to
	// "/mcp".
	m := metrics.New()

	handler := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Many distinct MCP sub-paths must all share one label.
	for _, sub := range []string{"/mcp", "/mcp/session-abc", "/mcp/session-def"} {
	        func(sub string) {
	                resp, err := http.Get(srv.URL + sub)
	                require.NoError(t, err)
	                defer resp.Body.Close()
	        }(sub)
	}

	// Gather metrics
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	var found bool
	for _, fam := range families {
		if fam.GetName() == "vibed_http_requests_total" {
			for _, metric := range fam.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "path" {
						val := label.GetValue()
						if val == "/mcp" {
							found = true
						}
						// Session IDs must never leak into the label.
						assert.NotContains(t, val, "session-abc",
							"path label should be normalized, got %q", val)
					}
				}
			}
		}
	}
	assert.True(t, found, "should find the normalized /mcp label")
}
