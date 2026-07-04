package runneragent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestAgent returns an Agent with a temp workdir and an httptest server
// driving its control handler.
func newTestAgent(t *testing.T, token string) (*Agent, *httptest.Server) {
	t.Helper()
	a := New(Config{
		Workdir:   t.TempDir(),
		Token:     token,
		AppPort:   8080,
		StopGrace: time.Second,
	})
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { a.stopProcess() })
	return a, srv
}

func do(t *testing.T, srv *httptest.Server, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

// waitState polls /status until the agent reports want, or fails after timeout.
func waitState(t *testing.T, srv *httptest.Server, token, want string) StatusResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last StatusResponse
	for time.Now().Before(deadline) {
		_, data := do(t, srv, http.MethodGet, PathStatus, token, nil)
		_ = json.Unmarshal(data, &last)
		if last.State == want {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state %q not reached, last = %+v", want, last)
	return last
}

func TestInfoReportsLanguageAndRuntimeProbe(t *testing.T) {
	a := New(Config{Workdir: t.TempDir(), Language: "node", RuntimeProbe: "echo v24.1.0"})
	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	// /info must be reachable WITHOUT a token (read-only metadata).
	resp, data := do(t, srv, http.MethodGet, PathInfo, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/info status = %d: %s", resp.StatusCode, data)
	}
	var info InfoResponse
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.Language != "node" {
		t.Errorf("language = %q, want node", info.Language)
	}
	if info.AgentContract != AgentContract {
		t.Errorf("agentContract = %q, want %q", info.AgentContract, AgentContract)
	}
	if !info.RuntimeProbeOK {
		t.Errorf("runtimeProbeOK = false, want true (out=%q)", info.RuntimeProbeOutput)
	}
	if info.RuntimeProbeOutput != "v24.1.0" {
		t.Errorf("runtimeProbeOutput = %q, want v24.1.0", info.RuntimeProbeOutput)
	}
}

func TestInfoRuntimeProbeFails(t *testing.T) {
	a := New(Config{Workdir: t.TempDir(), Language: "node", RuntimeProbe: "vibed-no-such-binary --version"})
	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	_, data := do(t, srv, http.MethodGet, PathInfo, "", nil)
	var info InfoResponse
	_ = json.Unmarshal(data, &info)
	if info.RuntimeProbeOK {
		t.Errorf("runtimeProbeOK = true for a missing binary, want false")
	}
}

func TestInjectRunsAndStops(t *testing.T) {
	a, srv := newTestAgent(t, "")

	resp, data := do(t, srv, http.MethodPost, PathInject, "", InjectRequest{
		Command: []string{"sh", "-c", "echo hello-from-app; sleep 30"},
		Files:   map[string]string{"marker.txt": "present"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inject status = %d, body = %s", resp.StatusCode, data)
	}

	st := waitState(t, srv, "", StateRunning)
	if st.PID == 0 {
		t.Fatalf("expected a PID while running, got %+v", st)
	}

	// File was written into the workdir.
	if _, err := os.Stat(filepath.Join(a.cfg.Workdir, "marker.txt")); err != nil {
		t.Fatalf("injected file not written: %v", err)
	}

	// Process output was captured.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, logData := do(t, srv, http.MethodGet, PathLogs, "", nil)
		var lr LogsResponse
		_ = json.Unmarshal(logData, &lr)
		if len(lr.Lines) > 0 && lr.Lines[0] == "hello-from-app" {
			break
		}
		time.Sleep(20 * time.Millisecond)
		if time.Now().After(deadline) {
			t.Fatalf("expected captured log line, got %v", lr.Lines)
		}
	}

	// Stop returns the agent to idle.
	resp, _ = do(t, srv, http.MethodPost, PathStop, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d", resp.StatusCode)
	}
	st = waitState(t, srv, "", StateIdle)
	if st.PID != 0 {
		t.Fatalf("expected no PID after stop, got %+v", st)
	}
}

func TestInjectFailedProcess(t *testing.T) {
	_, srv := newTestAgent(t, "")
	do(t, srv, http.MethodPost, PathInject, "", InjectRequest{
		Command: []string{"sh", "-c", "exit 3"},
		Files:   map[string]string{"x": "y"},
	})
	st := waitState(t, srv, "", StateFailed)
	if st.ExitCode == nil || *st.ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %+v", st)
	}
}

func TestReinjectReplacesProcess(t *testing.T) {
	_, srv := newTestAgent(t, "")
	do(t, srv, http.MethodPost, PathInject, "", InjectRequest{
		Command: []string{"sh", "-c", "sleep 30"},
		Files:   map[string]string{"a": "1"},
	})
	first := waitState(t, srv, "", StateRunning)

	do(t, srv, http.MethodPost, PathInject, "", InjectRequest{
		Command: []string{"sh", "-c", "sleep 30"},
		Files:   map[string]string{"b": "2"},
	})
	second := waitState(t, srv, "", StateRunning)

	if first.PID == second.PID {
		t.Fatalf("re-inject should start a new process, both PIDs = %d", first.PID)
	}
}

func TestInjectRejectsUnrunnableLanguage(t *testing.T) {
	_, srv := newTestAgent(t, "")
	resp, data := do(t, srv, http.MethodPost, PathInject, "", InjectRequest{
		Language: "go", // compiled — no fast-path run command, and no explicit Command
		Files:    map[string]string{"main.go": "package main"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unrunnable language, got %d: %s", resp.StatusCode, data)
	}
}

func TestAuthRequired(t *testing.T) {
	_, srv := newTestAgent(t, "s3cret")

	// Missing token → 401.
	resp, _ := do(t, srv, http.MethodGet, PathStatus, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}
	// Correct token → 200.
	resp, _ = do(t, srv, http.MethodGet, PathStatus, "s3cret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d", resp.StatusCode)
	}
	// Healthz is unauthenticated.
	resp, _ = do(t, srv, http.MethodGet, PathHealthz, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz should be unauthenticated, got %d", resp.StatusCode)
	}
}

// TestRequireAuthFailsClosed verifies that with no token configured, the control
// API is open by default (legacy single-node behavior) but fails closed when
// RequireAuth is set — while the health probe stays open in both cases.
func TestRequireAuthFailsClosed(t *testing.T) {
	newSrv := func(cfg Config) *httptest.Server {
		cfg.Workdir = t.TempDir()
		cfg.AppPort = 8080
		cfg.StopGrace = time.Second
		a := New(cfg)
		srv := httptest.NewServer(a.handler())
		t.Cleanup(srv.Close)
		t.Cleanup(func() { a.stopProcess() })
		return srv
	}

	// No token, RequireAuth off → control API open (unchanged default).
	open := newSrv(Config{})
	if resp, _ := do(t, open, http.MethodGet, PathStatus, "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("no-token/require-off: expected 200 (open), got %d", resp.StatusCode)
	}

	// No token, RequireAuth on → control API fails closed.
	closed := newSrv(Config{RequireAuth: true})
	if resp, _ := do(t, closed, http.MethodGet, PathStatus, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token/require-on: expected 401 (fail closed), got %d", resp.StatusCode)
	}
	if resp, _ := do(t, closed, http.MethodPost, PathInject, "", map[string]any{"command": []string{"x"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token/require-on: inject expected 401, got %d", resp.StatusCode)
	}
	// Health probe stays open even when fail-closed.
	if resp, _ := do(t, closed, http.MethodGet, PathHealthz, "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("no-token/require-on: healthz must stay open, got %d", resp.StatusCode)
	}
}

func TestWriteFilesRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../escape", "/etc/passwd", "a/../../escape"} {
		if err := writeFiles(root, map[string]string{bad: "x"}); err == nil {
			t.Errorf("writeFiles(%q) = nil error, want rejection", bad)
		}
	}
	// Nested-but-safe path is allowed.
	if err := writeFiles(root, map[string]string{"sub/dir/file.txt": "ok"}); err != nil {
		t.Fatalf("writeFiles nested path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub/dir/file.txt")); err != nil {
		t.Fatalf("nested file not written: %v", err)
	}
}

func TestBuildEnvForcesPort(t *testing.T) {
	env := buildEnv(map[string]string{"FOO": "bar"}, 9999)
	var sawPort, sawFoo bool
	for _, kv := range env {
		switch kv {
		case "PORT=9999":
			sawPort = true
		case "FOO=bar":
			sawFoo = true
		}
	}
	if !sawPort {
		t.Error("buildEnv did not set PORT")
	}
	if !sawFoo {
		t.Error("buildEnv did not include extra env")
	}
}
