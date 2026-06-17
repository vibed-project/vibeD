package workerd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// stubFetcher writes a fixed file map into the target dir, standing in for a
// real tarball download.
type stubFetcher struct {
	files map[string]string
	err   error
}

func (s stubFetcher) Fetch(_ context.Context, _ string, dir string) error {
	if s.err != nil {
		return s.err
	}
	for name, content := range s.files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func newHandler(t *testing.T, files map[string]string, fetchErr error) (*Handler, *Loader) {
	t.Helper()
	l, err := New(Options{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &Handler{Loader: l, Token: "tok", Fetcher: stubFetcher{files: files, err: fetchErr}}
	return h, l
}

func TestDeployExtractsWorkerAndReturnsPort(t *testing.T) {
	h, l := newHandler(t, map[string]string{
		"worker.js": "export default { fetch() {} }",
		"README.md": "ignore me",
	}, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	body, _ := json.Marshal(DeployRequest{AppID: "abc", SourceURL: "http://x/s.tgz"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/deploy", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var dr DeployResponse
	_ = json.NewDecoder(resp.Body).Decode(&dr)
	if dr.Port == 0 {
		t.Error("expected a non-zero port")
	}
	if l.Port("abc") != dr.Port {
		t.Errorf("loader port %d != response port %d", l.Port("abc"), dr.Port)
	}
}

func TestDeployHonorsExplicitEntrypoint(t *testing.T) {
	h, _ := newHandler(t, map[string]string{"src/main.js": "export default {}"}, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	body, _ := json.Marshal(DeployRequest{AppID: "e", SourceURL: "http://x", Entrypoint: "src/main.js"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/deploy", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestDeployNoEntryModuleIs400(t *testing.T) {
	h, _ := newHandler(t, map[string]string{"notaworker.txt": "x"}, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	body, _ := json.Marshal(DeployRequest{AppID: "e", SourceURL: "http://x"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/deploy", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDeployRequiresAuth(t *testing.T) {
	h, _ := newHandler(t, map[string]string{"worker.js": "x"}, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	body, _ := json.Marshal(DeployRequest{AppID: "e", SourceURL: "http://x"})
	resp, err := http.Post(srv.URL+"/deploy", "application/json", bytes.NewReader(body)) // no token
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestDeleteRemovesApp(t *testing.T) {
	h, l := newHandler(t, map[string]string{"worker.js": "x"}, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	// Deploy then delete.
	body, _ := json.Marshal(DeployRequest{AppID: "gone", SourceURL: "http://x"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/deploy", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	r1, _ := http.DefaultClient.Do(req)
	r1.Body.Close()
	if l.Port("gone") == 0 {
		t.Fatal("deploy didn't register app")
	}

	dreq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/apps/gone", nil)
	dreq.Header.Set("Authorization", "Bearer tok")
	r2, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", r2.StatusCode)
	}
	if l.Port("gone") != 0 {
		t.Error("app still registered after delete")
	}
}

func TestHealthzNoAuth(t *testing.T) {
	h, _ := newHandler(t, nil, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", resp.StatusCode)
	}
}
