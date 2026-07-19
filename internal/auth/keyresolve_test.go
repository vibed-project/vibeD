package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
)

// serveAPIKeyAuth builds the apikey middleware for keys and returns a server
// whose handler just 200s, so tests can probe which bearer tokens authenticate.
func serveAPIKeyAuth(t *testing.T, keys []config.APIKeyConf) *httptest.Server {
	t.Helper()
	cfg := config.AuthConfig{Enabled: true, Mode: "apikey", APIKeys: keys}
	mw, _, err := Build(cfg, nil, discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	t.Cleanup(srv.Close)
	return srv
}

// bearerStatus performs a request with the given bearer token and returns the
// response status code.
func bearerStatus(t *testing.T, srv *httptest.Server, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestStaticKeys_FileAndEnvKeysAuthenticate: "file:" and "env:" key references
// still authenticate after the move to startup-time resolution.
func TestStaticKeys_FileAndEnvKeysAuthenticate(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "apikey")
	if err := os.WriteFile(keyFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
	t.Setenv("VIBED_TEST_STARTUP_KEY", "env-secret")

	srv := serveAPIKeyAuth(t, []config.APIKeyConf{
		{Key: "file:" + keyFile, Name: "file-key"},
		{Key: "env:VIBED_TEST_STARTUP_KEY", Name: "env-key"},
	})

	if code := bearerStatus(t, srv, "file-secret"); code != http.StatusOK {
		t.Errorf("file: key = %d, want 200", code)
	}
	if code := bearerStatus(t, srv, "env-secret"); code != http.StatusOK {
		t.Errorf("env: key = %d, want 200", code)
	}
	if code := bearerStatus(t, srv, "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", code)
	}
}

// TestStaticKeys_ResolvedOnceAtStartup locks in the perf fix: the secret file
// is read when the verifier is built, not per request. After rotating the
// file's content, the originally resolved secret must keep authenticating and
// the new content must not — proving no request-time filesystem read remains.
func TestStaticKeys_ResolvedOnceAtStartup(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "apikey")
	if err := os.WriteFile(keyFile, []byte("original-secret"), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}

	srv := serveAPIKeyAuth(t, []config.APIKeyConf{{Key: "file:" + keyFile, Name: "rotating"}})

	if code := bearerStatus(t, srv, "original-secret"); code != http.StatusOK {
		t.Fatalf("before rotation: got %d, want 200", code)
	}

	if err := os.WriteFile(keyFile, []byte("rotated-secret"), 0o600); err != nil {
		t.Fatalf("rotating key file: %v", err)
	}
	if code := bearerStatus(t, srv, "original-secret"); code != http.StatusOK {
		t.Errorf("startup-resolved secret after rotation: got %d, want 200", code)
	}
	if code := bearerStatus(t, srv, "rotated-secret"); code != http.StatusUnauthorized {
		t.Errorf("rotated file content: got %d, want 401 (file must not be re-read per request)", code)
	}
}

// TestStaticKeys_UnresolvableKeyDoesNotBreakOthers: a key whose reference fails
// to resolve at startup (missing file) is logged and falls back to its literal
// value, and the remaining keys keep authenticating.
func TestStaticKeys_UnresolvableKeyDoesNotBreakOthers(t *testing.T) {
	srv := serveAPIKeyAuth(t, []config.APIKeyConf{
		{Key: "file:" + filepath.Join(t.TempDir(), "does-not-exist"), Name: "broken"},
		{Key: "good-secret", Name: "good"},
	})

	if code := bearerStatus(t, srv, "good-secret"); code != http.StatusOK {
		t.Errorf("literal key next to an unresolvable one: got %d, want 200", code)
	}
}

// TestOAuthPassthrough_ResolvedOnceAtStartup: the oauth proxy-secret loop got
// the same treatment — the file: secret is resolved at construction, so a
// rotated file does not change which token the verifier accepts.
func TestOAuthPassthrough_ResolvedOnceAtStartup(t *testing.T) {
	logger := discardLogger()
	keyFile := filepath.Join(t.TempDir(), "proxysecret")
	if err := os.WriteFile(keyFile, []byte("proxy-original"), 0o600); err != nil {
		t.Fatalf("writing secret file: %v", err)
	}

	keys := []config.APIKeyConf{{Key: "file:" + keyFile, Name: "proxy"}}
	verifier := oauthPassthroughVerifier(resolveAPIKeys(keys, logger), nil, logger)

	if err := os.WriteFile(keyFile, []byte("proxy-rotated"), 0o600); err != nil {
		t.Fatalf("rotating secret file: %v", err)
	}

	r := httptest.NewRequest("GET", "/mcp", nil)
	if _, err := verifier(context.Background(), "proxy-original", r); err != nil {
		t.Errorf("startup-resolved proxy secret rejected after rotation: %v", err)
	}
	if _, err := verifier(context.Background(), "proxy-rotated", r); err == nil {
		t.Error("rotated file content accepted: secret must not be re-read per request")
	}
}
