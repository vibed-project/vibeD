//go:build integration

package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	resp, err := http.Get(srv.URL + "/api/artifacts")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(srv.URL + "/api/artifacts/abc123")
	require.NoError(t, err)
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	resp.Body.Close()

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
	// We test normalizePath indirectly through the middleware by checking
	// that different dynamic paths get grouped under the same label.
	m := metrics.New()

	handler := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Make requests to different artifact IDs
	for _, id := range []string{"abc123", "def456", "ghi789"} {
		resp, err := http.Get(srv.URL + "/api/artifacts/" + id)
		require.NoError(t, err)
		resp.Body.Close()
	}

	// Make a logs request
	resp, err := http.Get(srv.URL + "/api/artifacts/abc123/logs")
	require.NoError(t, err)
	resp.Body.Close()

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
						// Should be normalized, not raw paths
						if val == "/api/artifacts/:id" || val == "/api/artifacts/:id/logs" {
							found = true
						}
						// Should NOT contain actual IDs
						assert.False(t, strings.Contains(val, "abc123"),
							"path label should be normalized, got %q", val)
					}
				}
			}
		}
	}
	assert.True(t, found, "should find normalized path labels")
}
