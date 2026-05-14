package deployer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/pool"
	"github.com/vibed-project/vibeD/internal/runneragent"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/pkg/api"
)

// --- stubs ---

type stubPool struct {
	mu       sync.Mutex
	claimErr error
	claimed  []*pool.Runner
	released []*pool.Runner
}

func (s *stubPool) Claim(_ context.Context, language string) (*pool.Runner, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	r := &pool.Runner{Name: "runner-" + language, Namespace: "vibed-runners", Language: language}
	s.mu.Lock()
	s.claimed = append(s.claimed, r)
	s.mu.Unlock()
	return r, nil
}

func (s *stubPool) Release(_ context.Context, r *pool.Runner) {
	s.mu.Lock()
	s.released = append(s.released, r)
	s.mu.Unlock()
}

func (s *stubPool) releaseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.released)
}

type stubAgent struct {
	injectErr  error
	injectResp *runneragent.StatusResponse
	statusResp *runneragent.StatusResponse
	logsResp   *runneragent.LogsResponse
	injected   *runneragent.InjectRequest
}

func (s *stubAgent) Inject(_ context.Context, req runneragent.InjectRequest) (*runneragent.StatusResponse, error) {
	s.injected = &req
	if s.injectErr != nil {
		return nil, s.injectErr
	}
	if s.injectResp != nil {
		return s.injectResp, nil
	}
	return &runneragent.StatusResponse{State: runneragent.StateRunning}, nil
}

func (s *stubAgent) Status(context.Context) (*runneragent.StatusResponse, error) {
	if s.statusResp != nil {
		return s.statusResp, nil
	}
	return &runneragent.StatusResponse{State: runneragent.StateRunning}, nil
}

func (s *stubAgent) Logs(context.Context, int) (*runneragent.LogsResponse, error) {
	if s.logsResp != nil {
		return s.logsResp, nil
	}
	return &runneragent.LogsResponse{}, nil
}

// newTestDeployer wires a RunnerDeployer onto a stub pool, stub agent, and real
// local storage seeded with the given source files.
func newTestDeployer(t *testing.T, sp *stubPool, sa *stubAgent, files map[string]string) (*RunnerDeployer, *api.Artifact) {
	t.Helper()
	stg, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	artifact := &api.Artifact{ID: "art-1", Name: "demo", Language: "python", Port: 8080}
	if _, err := stg.StoreSource(context.Background(), artifact.ID, files); err != nil {
		t.Fatalf("StoreSource: %v", err)
	}
	d := &RunnerDeployer{
		pool:          sp,
		storage:       stg,
		token:         "tok",
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		newClient:     func(string, string) agentClient { return sa },
		confirmWindow: 10 * time.Millisecond,
		runners:       make(map[string]*pool.Runner),
	}
	return d, artifact
}

func TestRunnerDeploySuccess(t *testing.T) {
	sp := &stubPool{}
	sa := &stubAgent{}
	d, artifact := newTestDeployer(t, sp, sa, map[string]string{"app.py": "print('hi')"})

	res, err := d.Deploy(context.Background(), artifact)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !strings.Contains(res.URL, "runner-python") {
		t.Errorf("URL %q should point at the claimed runner", res.URL)
	}
	// The injected request carried the source and language.
	if sa.injected == nil || sa.injected.Language != "python" || sa.injected.Files["app.py"] == "" {
		t.Fatalf("inject request not as expected: %+v", sa.injected)
	}
	// The runner is now on record for the artifact.
	if _, err := d.GetURL(context.Background(), artifact); err != nil {
		t.Errorf("GetURL after deploy: %v", err)
	}
}

func TestRunnerDeployReleasesOnInjectFailure(t *testing.T) {
	sp := &stubPool{}
	sa := &stubAgent{injectErr: errors.New("connection refused")}
	d, artifact := newTestDeployer(t, sp, sa, map[string]string{"app.py": "x"})

	if _, err := d.Deploy(context.Background(), artifact); err == nil {
		t.Fatal("Deploy should fail when inject fails")
	}
	if sp.releaseCount() != 1 {
		t.Errorf("failed inject should release the runner; releases = %d", sp.releaseCount())
	}
}

func TestRunnerDeployFailsWhenProcessCrashes(t *testing.T) {
	sp := &stubPool{}
	// Inject reports running, but the confirmation poll sees it failed.
	sa := &stubAgent{
		statusResp: &runneragent.StatusResponse{State: runneragent.StateFailed, Error: "boom"},
		logsResp:   &runneragent.LogsResponse{Lines: []string{"Traceback ..."}},
	}
	d, artifact := newTestDeployer(t, sp, sa, map[string]string{"app.py": "x"})

	_, err := d.Deploy(context.Background(), artifact)
	if err == nil {
		t.Fatal("Deploy should fail when the process crashes during confirmation")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q should include the agent failure reason", err)
	}
	if sp.releaseCount() != 1 {
		t.Errorf("crashed process should release the runner; releases = %d", sp.releaseCount())
	}
}

func TestRunnerDeployRejectsUnsupportedLanguage(t *testing.T) {
	sp := &stubPool{claimErr: pool.ErrLanguageUnsupported}
	d, artifact := newTestDeployer(t, sp, &stubAgent{}, map[string]string{"app.py": "x"})

	if _, err := d.Deploy(context.Background(), artifact); err == nil {
		t.Fatal("Deploy should fail when the pool cannot claim a runner")
	}
}

func TestRunnerDeleteReleasesRunner(t *testing.T) {
	sp := &stubPool{}
	d, artifact := newTestDeployer(t, sp, &stubAgent{}, map[string]string{"app.py": "x"})

	if _, err := d.Deploy(context.Background(), artifact); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := d.Delete(context.Background(), artifact); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if sp.releaseCount() != 1 {
		t.Errorf("Delete should release the runner; releases = %d", sp.releaseCount())
	}
	// Delete with no runner on record is a no-op, not an error.
	if err := d.Delete(context.Background(), artifact); err != nil {
		t.Errorf("second Delete should be a no-op: %v", err)
	}
}

func TestRunnerUpdateRedeploysWhenNoRunnerOnRecord(t *testing.T) {
	sp := &stubPool{}
	sa := &stubAgent{}
	d, artifact := newTestDeployer(t, sp, sa, map[string]string{"app.py": "x"})

	// Update with nothing on record falls back to a fresh claim + inject.
	if _, err := d.Update(context.Background(), artifact); err != nil {
		t.Fatalf("Update: %v", err)
	}
	sp.mu.Lock()
	claims := len(sp.claimed)
	sp.mu.Unlock()
	if claims != 1 {
		t.Errorf("Update without a record should claim a runner; claims = %d", claims)
	}
}

func TestRunnerGetLogs(t *testing.T) {
	sp := &stubPool{}
	sa := &stubAgent{logsResp: &runneragent.LogsResponse{Lines: []string{"line one", "line two"}}}
	d, artifact := newTestDeployer(t, sp, sa, map[string]string{"app.py": "x"})

	if _, err := d.Deploy(context.Background(), artifact); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	lines, err := d.GetLogs(context.Background(), artifact, 10)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(lines) != 2 || lines[0] != "line one" {
		t.Errorf("GetLogs = %v, want the agent's lines", lines)
	}
}
