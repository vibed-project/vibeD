package frontend_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/frontend"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/store"

	"github.com/stretchr/testify/assert"
)

// TestArtifactDeprecationHeaders verifies the legacy /api/artifacts routes
// carry the deprecation advisory headers while the routes that stay
// (e.g. /api/whoami) do not. A single handler is shared across subtests
// because metrics.New registers Prometheus collectors on the default
// registry and panics on a second call in the same process.
func TestArtifactDeprecationHeaders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewEventBus()
	m := metrics.New()
	// Minimal orchestrator backed by an in-memory store — no cluster needed
	// for the list/whoami routes exercised here.
	orch := orchestrator.NewOrchestrator(cfg, nil, deployer.NewFactory(), nil, store.NewMemoryStore(), nil, m, nil, bus, nil, logger)
	handler := frontend.NewHandler(orch, nil, cfg, bus, m, nil, nil)

	t.Run("legacy artifact routes carry deprecation headers", func(t *testing.T) {
		for _, path := range []string{"/api/artifacts", "/api/artifacts/"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code, "path %s", path)
			assert.Equal(t, "true", rec.Header().Get("Deprecation"), "path %s", path)
			assert.Equal(t, "/v1/apps", rec.Header().Get("X-Deprecated-Use"), "path %s", path)
		}
	})

	t.Run("kept routes have no deprecation headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Deprecation"))
		assert.Empty(t, rec.Header().Get("X-Deprecated-Use"))
	})
}
