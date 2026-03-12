//go:build integration

package auth_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"alive"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("# metrics"))
	})
	mux.HandleFunc("/mcp/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"mcp":"ok"}`))
	})
	mux.HandleFunc("/api/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("frontend"))
	})
	return mux
}

func TestAuth_Disabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := config.AuthConfig{Enabled: false}

	middleware, err := vibedauth.Middleware(cfg, logger)
	require.NoError(t, err)

	handler := middleware(testHandler())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// All paths should be accessible without auth
	for _, path := range []string{"/healthz", "/readyz", "/metrics", "/mcp/", "/api/artifacts", "/"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "path %s should be accessible", path)
		resp.Body.Close()
	}
}

func TestAuth_ValidAPIKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := config.AuthConfig{
		Enabled: true,
		Mode:    "apikey",
		APIKeys: []config.APIKeyConf{
			{Key: "test-secret-key", Name: "test"},
		},
	}

	middleware, err := vibedauth.Middleware(cfg, logger)
	require.NoError(t, err)

	handler := vibedauth.SkipAuthPaths(middleware)(testHandler())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer test-secret-key")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestAuth_InvalidAPIKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := config.AuthConfig{
		Enabled: true,
		Mode:    "apikey",
		APIKeys: []config.APIKeyConf{
			{Key: "correct-key", Name: "test"},
		},
	}

	middleware, err := vibedauth.Middleware(cfg, logger)
	require.NoError(t, err)

	handler := vibedauth.SkipAuthPaths(middleware)(testHandler())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

func TestAuth_MissingToken(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := config.AuthConfig{
		Enabled: true,
		Mode:    "apikey",
		APIKeys: []config.APIKeyConf{
			{Key: "some-key", Name: "test"},
		},
	}

	middleware, err := vibedauth.Middleware(cfg, logger)
	require.NoError(t, err)

	handler := vibedauth.SkipAuthPaths(middleware)(testHandler())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// No Authorization header
	resp, err := http.Get(srv.URL + "/api/artifacts")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

func TestAuth_SkipHealthPaths(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := config.AuthConfig{
		Enabled: true,
		Mode:    "apikey",
		APIKeys: []config.APIKeyConf{
			{Key: "some-key", Name: "test"},
		},
	}

	middleware, err := vibedauth.Middleware(cfg, logger)
	require.NoError(t, err)

	handler := vibedauth.SkipAuthPaths(middleware)(testHandler())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Health and metrics paths should be accessible without auth
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "path %s should be accessible without auth", path)
		resp.Body.Close()
	}
}

func TestAuth_ProtectedPaths(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := config.AuthConfig{
		Enabled: true,
		Mode:    "apikey",
		APIKeys: []config.APIKeyConf{
			{Key: "some-key", Name: "test"},
		},
	}

	middleware, err := vibedauth.Middleware(cfg, logger)
	require.NoError(t, err)

	handler := vibedauth.SkipAuthPaths(middleware)(testHandler())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// MCP and API paths should require auth
	for _, path := range []string{"/mcp/", "/api/artifacts"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "path %s should require auth", path)
		resp.Body.Close()
	}
}

func TestAuth_EnvVarKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Set env var
	t.Setenv("VIBED_TEST_KEY", "env-resolved-key")

	cfg := config.AuthConfig{
		Enabled: true,
		Mode:    "apikey",
		APIKeys: []config.APIKeyConf{
			{Key: "env:VIBED_TEST_KEY", Name: "env-test"},
		},
	}

	middleware, err := vibedauth.Middleware(cfg, logger)
	require.NoError(t, err)

	handler := vibedauth.SkipAuthPaths(middleware)(testHandler())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Use the resolved env var value as the bearer token
	req, _ := http.NewRequest("GET", srv.URL+"/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer env-resolved-key")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
