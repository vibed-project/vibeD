package preview

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vibed-project/vibeD/pkg/api"
)

type stubSource struct {
	mu       sync.Mutex
	artifact *api.Artifact
	err      error
	calls    int
}

func (s *stubSource) Status(_ context.Context, _ string) (*api.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.artifact, nil
}

func TestSplitPreviewPath(t *testing.T) {
	tests := []struct {
		path         string
		wantID       string
		wantUpstream string
		wantOK       bool
	}{
		{"/preview/abc123/", "abc123", "/", true},
		{"/preview/abc123/index.html", "abc123", "/index.html", true},
		{"/preview/abc123/foo/bar?q=1", "abc123", "/foo/bar?q=1", true},
		{"/preview/abc123", "abc123", "", true}, // caller redirects
		{"/preview/", "", "", false},
		{"/preview", "", "", false},
		{"/something-else", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			id, up, ok := splitPreviewPath(tt.path)
			if id != tt.wantID || up != tt.wantUpstream || ok != tt.wantOK {
				t.Errorf("splitPreviewPath(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tt.path, id, up, ok, tt.wantID, tt.wantUpstream, tt.wantOK)
			}
		})
	}
}

// stubUpstream returns a test server that records incoming requests and serves
// a body identifying the path it saw, so tests can assert the proxy rewrote
// the path correctly.
func stubUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back details that let the test check the proxied request.
		w.Header().Set("X-Saw-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Saw-Cookie", r.Header.Get("Cookie"))
		w.Header().Set("X-Saw-Vibed-Artifact", r.Header.Get("X-Vibed-Artifact-Id"))
		w.Header().Set("X-Saw-Forwarded-Prefix", r.Header.Get("X-Forwarded-Prefix"))
		w.Header().Set("X-Saw-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
		_, _ = io.WriteString(w, "upstream-saw:"+r.Method+":"+r.URL.Path+"?"+r.URL.RawQuery)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestHandler(t *testing.T, src artifactSource) *Handler {
	t.Helper()
	return New(src, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestHandlerProxiesRunningPreview(t *testing.T) {
	upstream := stubUpstream(t)
	src := &stubSource{
		artifact: &api.Artifact{
			ID: "abc", Status: api.StatusRunning,
			Mode: api.ModePreview, Target: api.TargetRunner,
			URL: upstream.URL,
		},
	}
	h := newTestHandler(t, src)

	req := httptest.NewRequest(http.MethodGet, "/preview/abc/foo/bar?q=1", nil)
	req.Header.Set("Authorization", "Bearer secret-vibed-token")
	req.Header.Set("Cookie", "session=secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "upstream-saw:GET:/foo/bar?q=1" {
		t.Errorf("upstream saw %q, want it rewritten under /foo/bar?q=1", got)
	}
	// vibeD's auth must not leak to the user app.
	if w.Header().Get("X-Saw-Auth") != "" {
		t.Error("Authorization header was leaked to upstream")
	}
	if w.Header().Get("X-Saw-Cookie") != "" {
		t.Error("Cookie header was leaked to upstream")
	}
	if got := w.Header().Get("X-Saw-Vibed-Artifact"); got != "abc" {
		t.Errorf("X-Vibed-Artifact-Id = %q, want abc", got)
	}
	if got := w.Header().Get("X-Saw-Forwarded-Prefix"); got != "/preview/abc" {
		t.Errorf("X-Forwarded-Prefix = %q, want /preview/abc", got)
	}
	if got := w.Header().Get("X-Saw-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http", got)
	}
}

func TestHandlerRedirectsMissingTrailingSlash(t *testing.T) {
	src := &stubSource{artifact: &api.Artifact{ID: "abc", Status: api.StatusRunning, Mode: api.ModePreview, Target: api.TargetRunner, URL: "http://x"}}
	h := newTestHandler(t, src)

	req := httptest.NewRequest(http.MethodGet, "/preview/abc?q=1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/preview/abc/?q=1" {
		t.Errorf("Location = %q, want /preview/abc/?q=1", loc)
	}
	if src.calls != 0 {
		t.Errorf("Status was called %d times before redirect, want 0", src.calls)
	}
}

func TestHandlerNotFoundForUnknownArtifact(t *testing.T) {
	src := &stubSource{err: errors.New("not found")}
	h := newTestHandler(t, src)

	req := httptest.NewRequest(http.MethodGet, "/preview/missing/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (don't leak existence)", w.Code)
	}
}

func TestHandlerRejectsNonRunnerArtifact(t *testing.T) {
	src := &stubSource{
		artifact: &api.Artifact{ID: "abc", Status: api.StatusRunning, Mode: api.ModeBuilt, Target: api.TargetKnative, URL: "http://x"},
	}
	h := newTestHandler(t, src)

	req := httptest.NewRequest(http.MethodGet, "/preview/abc/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for non-preview artifact", w.Code)
	}
	if !strings.Contains(w.Body.String(), "fast-path preview") {
		t.Errorf("body %q should explain the rejection", w.Body.String())
	}
}

func TestHandlerRejectsArtifactNotRunning(t *testing.T) {
	src := &stubSource{
		artifact: &api.Artifact{ID: "abc", Status: api.StatusBuilding, Mode: api.ModePreview, Target: api.TargetRunner, URL: "http://x"},
	}
	h := newTestHandler(t, src)

	req := httptest.NewRequest(http.MethodGet, "/preview/abc/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when not running", w.Code)
	}
}

func TestHandlerBadGatewayWhenUpstreamUnreachable(t *testing.T) {
	src := &stubSource{
		artifact: &api.Artifact{ID: "abc", Status: api.StatusRunning, Mode: api.ModePreview, Target: api.TargetRunner,
			URL: "http://127.0.0.1:1"}, // nothing listening
	}
	h := newTestHandler(t, src)

	req := httptest.NewRequest(http.MethodGet, "/preview/abc/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 when upstream is unreachable", w.Code)
	}
}
