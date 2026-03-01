//go:build integration

package frontend_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/maxkorbacher/vibed/internal/config"
	"github.com/maxkorbacher/vibed/internal/deployer"
	"github.com/maxkorbacher/vibed/internal/environment"
	"github.com/maxkorbacher/vibed/internal/frontend"
	"github.com/maxkorbacher/vibed/internal/metrics"
	"github.com/maxkorbacher/vibed/internal/orchestrator"
	"github.com/maxkorbacher/vibed/internal/storage"
	"github.com/maxkorbacher/vibed/internal/store"
	"github.com/maxkorbacher/vibed/pkg/api"
	"github.com/maxkorbacher/vibed/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: environment import used by TestAPI_ListTargets

func TestAPI_ListArtifacts_Empty(t *testing.T) {
	orch := testAPIOrchSimple(t)
	handler := frontend.NewHandler(orch)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/artifacts")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var artifacts []api.ArtifactSummary
	err = json.NewDecoder(resp.Body).Decode(&artifacts)
	require.NoError(t, err)
	assert.Empty(t, artifacts)
}

func TestAPI_ListTargets(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	tmpDir := t.TempDir()

	cfg := testutil.TestConfig("default", tmpDir)
	localStorage, err := storage.NewLocalStorage(tmpDir)
	require.NoError(t, err)
	memStore := store.NewMemoryStore()
	mockBuilder := &testutil.MockBuilder{}
	factory := deployer.NewFactory()
	detector := environment.NewDetector(clients, logger)
	m := metrics.New()

	orch := orchestrator.NewOrchestrator(cfg, detector, mockBuilder, factory, localStorage, memStore, m, logger)
	handler := frontend.NewHandler(orch)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/targets")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var targets []api.TargetInfo
	err = json.NewDecoder(resp.Body).Decode(&targets)
	require.NoError(t, err)
	assert.NotEmpty(t, targets)
}

func TestAPI_ArtifactNotFound(t *testing.T) {
	orch := testAPIOrchSimple(t)
	handler := frontend.NewHandler(orch)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/artifacts/nonexistent-id")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// testAPIOrchSimple creates a minimal orchestrator that doesn't need a cluster.
// It uses nil detector, which means ListTargets will panic — only use for
// tests that don't call ListTargets.
func testAPIOrchSimple(t *testing.T) *orchestrator.Orchestrator {
	t.Helper()
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := &config.Config{
		Deployment: config.DeploymentConfig{
			PreferredTarget: "kubernetes",
			Namespace:       "default",
		},
		Storage: config.StorageConfig{
			Backend: "local",
			Local:   config.LocalStorageConfig{BasePath: tmpDir},
		},
		Registry: config.RegistryConfig{Enabled: false},
	}

	localStorage, err := storage.NewLocalStorage(tmpDir)
	require.NoError(t, err)
	memStore := store.NewMemoryStore()
	mockBuilder := &testutil.MockBuilder{}
	factory := deployer.NewFactory()
	m := metrics.New()

	return orchestrator.NewOrchestrator(cfg, nil, mockBuilder, factory, localStorage, memStore, m, logger)
}
