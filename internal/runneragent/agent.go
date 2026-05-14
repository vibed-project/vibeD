package runneragent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vibed-project/vibeD/internal/appspec"
)

// Config configures an Agent.
//
// The agent is expected to run as a child of a minimal init (e.g. tini) inside
// the runner image, not as literal PID 1 — the init handles zombie reaping for
// any grandchildren the user process orphans. The agent only supervises its
// own direct child.
type Config struct {
	// ControlAddr is the listen address for the control API, e.g. ":9000".
	// This port must not be exposed through the Sandbox's public URL.
	ControlAddr string
	// Workdir is where injected source files are written and the user process
	// runs, e.g. "/workspace".
	Workdir string
	// Token, when non-empty, is required as a Bearer token on every control
	// API request. vibeD passes the same value via the VIBED_AGENT_TOKEN env.
	Token string
	// AppPort is the default port the user process should listen on; exported
	// to the process as PORT. An InjectRequest may override it per-deploy.
	AppPort int
	// LogLines is the capacity of the per-process log ring buffer.
	LogLines int
	// StopGrace is how long to wait after SIGTERM before SIGKILL when stopping
	// or replacing the user process.
	StopGrace time.Duration
	// Logger is the agent's own logger (not the user process's output).
	Logger *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.ControlAddr == "" {
		c.ControlAddr = ":9000"
	}
	if c.Workdir == "" {
		c.Workdir = "/workspace"
	}
	if c.AppPort == 0 {
		c.AppPort = 8080
	}
	if c.LogLines == 0 {
		c.LogLines = 500
	}
	if c.StopGrace == 0 {
		c.StopGrace = 5 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Agent runs the control API and supervises a single user process.
type Agent struct {
	cfg  Config
	logs *ringBuffer

	mu       sync.Mutex
	gen      uint64        // bumped on every inject; lets stale supervisors no-op
	cmd      *exec.Cmd     // current user process, nil when idle
	done     chan struct{} // closed by supervise() once cmd.Wait() has returned
	state    string
	exitCode *int
	command  []string
	appPort  int
	lastErr  string
}

// New returns a ready-to-Run Agent.
func New(cfg Config) *Agent {
	cfg.applyDefaults()
	return &Agent{
		cfg:   cfg,
		logs:  newRingBuffer(cfg.LogLines),
		state: StateIdle,
	}
}

// handler builds the control-API HTTP handler. Exposed separately from Run so
// tests can drive it via httptest without binding a port.
func (a *Agent) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealthz, a.handleHealthz) // unauthenticated liveness probe
	mux.HandleFunc(PathInject, a.auth(a.handleInject))
	mux.HandleFunc(PathStatus, a.auth(a.handleStatus))
	mux.HandleFunc(PathLogs, a.auth(a.handleLogs))
	mux.HandleFunc(PathStop, a.auth(a.handleStop))
	return mux
}

// Run starts the control API server and blocks until ctx is cancelled, then
// stops the user process and shuts the server down gracefully.
func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.Workdir, 0o755); err != nil {
		return fmt.Errorf("creating workdir %s: %w", a.cfg.Workdir, err)
	}

	srv := &http.Server{
		Addr:              a.cfg.ControlAddr,
		Handler:           a.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.cfg.Logger.Info("runner agent control API listening", "addr", a.cfg.ControlAddr, "workdir", a.cfg.Workdir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		a.cfg.Logger.Info("runner agent shutting down")
		a.stopProcess()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// auth wraps a handler with Bearer-token authentication when a token is set.
func (a *Agent) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != a.cfg.Token {
				writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
				return
			}
		}
		next(w, r)
	}
}

func (a *Agent) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (a *Agent) handleInject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	var req InjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file is required")
		return
	}

	status, err := a.inject(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *Agent) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.status())
}

func (a *Agent) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 0
	if v := r.URL.Query().Get("lines"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	writeJSON(w, http.StatusOK, LogsResponse{Lines: a.logs.snapshot(n)})
}

func (a *Agent) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	a.stopProcess()
	writeJSON(w, http.StatusOK, a.status())
}

// inject writes the source files and (re)starts the user process. Any process
// from a previous inject is stopped first.
func (a *Agent) inject(req InjectRequest) (StatusResponse, error) {
	// Resolve the run command outside the lock — pure computation.
	command := req.Command
	if len(command) == 0 {
		command = appspec.RunCommand(req.Language, req.Files)
	}
	if len(command) == 0 {
		return StatusResponse{}, fmt.Errorf("no run command: language %q is not runnable on the fast path and no explicit command was given", req.Language)
	}

	appPort := req.Port
	if appPort == 0 {
		appPort = a.cfg.AppPort
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Replace any existing process.
	a.stopProcessLocked()

	if err := a.resetWorkdirLocked(); err != nil {
		return StatusResponse{}, err
	}
	if err := writeFiles(a.cfg.Workdir, req.Files); err != nil {
		return StatusResponse{}, err
	}
	a.logs.reset()

	if err := a.startProcessLocked(command, req.Env, appPort); err != nil {
		a.state = StateFailed
		a.lastErr = err.Error()
		return a.statusLocked(), err
	}
	return a.statusLocked(), nil
}

// startProcessLocked launches the user process. Caller holds a.mu.
func (a *Agent) startProcessLocked(command []string, env map[string]string, appPort int) error {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = a.cfg.Workdir
	cmd.Env = buildEnv(env, appPort)
	cmd.Stdout = a.logs
	cmd.Stderr = a.logs
	// Own process group so we can signal the whole tree on stop.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting user process %v: %w", command, err)
	}

	a.gen++
	gen := a.gen
	done := make(chan struct{})
	a.cmd = cmd
	a.done = done
	a.state = StateRunning
	a.exitCode = nil
	a.command = command
	a.appPort = appPort
	a.lastErr = ""
	a.cfg.Logger.Info("user process started", "pid", cmd.Process.Pid, "command", command, "appPort", appPort)

	go a.supervise(cmd, gen, done)
	return nil
}

// supervise owns the single cmd.Wait() for a user process: it waits for the
// process to exit, signals completion by closing done (so stopProcessLocked
// never has to call Wait itself), and records the outcome — unless a newer
// inject has already superseded this process.
func (a *Agent) supervise(cmd *exec.Cmd, gen uint64, done chan struct{}) {
	err := cmd.Wait()
	close(done)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen {
		// A newer inject replaced this process; its exit is not our state.
		return
	}
	a.cmd = nil
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
			a.lastErr = err.Error()
		}
	}
	a.exitCode = &code
	if code == 0 {
		a.state = StateExited
	} else {
		a.state = StateFailed
	}
	a.cfg.Logger.Info("user process exited", "exitCode", code, "command", a.command)
}

// stopProcess stops the current user process, if any.
func (a *Agent) stopProcess() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopProcessLocked()
}

// stopProcessLocked sends SIGTERM to the process group, escalating to SIGKILL
// after StopGrace. It waits for the supervisor goroutine to observe the exit
// (via done) rather than calling cmd.Wait() itself — Wait must only be called
// once. Caller holds a.mu.
func (a *Agent) stopProcessLocked() {
	if a.cmd == nil || a.cmd.Process == nil {
		return
	}
	cmd := a.cmd
	done := a.done
	pid := cmd.Process.Pid
	// Bump the generation so the supervisor goroutine treats this exit as
	// superseded rather than recording a "failed" state.
	a.gen++
	a.cmd = nil
	a.done = nil
	a.state = StateIdle

	// Signal the whole process group (negative PID).
	_ = syscall.Kill(-pid, syscall.SIGTERM)

	select {
	case <-done:
	case <-time.After(a.cfg.StopGrace):
		a.cfg.Logger.Warn("user process did not stop in time, killing", "pid", pid)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	}
	a.cfg.Logger.Info("user process stopped", "pid", pid)
}

// resetWorkdirLocked empties the workdir without removing the directory itself
// (it may be a mounted volume). Caller holds a.mu.
func (a *Agent) resetWorkdirLocked() error {
	entries, err := os.ReadDir(a.cfg.Workdir)
	if err != nil {
		return fmt.Errorf("reading workdir: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(a.cfg.Workdir, e.Name())); err != nil {
			return fmt.Errorf("clearing workdir entry %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (a *Agent) status() StatusResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.statusLocked()
}

func (a *Agent) statusLocked() StatusResponse {
	s := StatusResponse{
		State:    a.state,
		ExitCode: a.exitCode,
		Command:  a.command,
		AppPort:  a.appPort,
		Error:    a.lastErr,
	}
	if a.cmd != nil && a.cmd.Process != nil {
		s.PID = a.cmd.Process.Pid
	}
	return s
}

// writeFiles writes the file map into root, rejecting any path that is
// absolute or escapes root via "..".
func writeFiles(root string, files map[string]string) error {
	for name, content := range files {
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("illegal file path %q", name)
		}
		dst := filepath.Join(root, clean)
		// Defence in depth: ensure the joined path is still within root.
		if rel, err := filepath.Rel(root, dst); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("illegal file path %q", name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("creating dir for %q: %w", name, err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %q: %w", name, err)
		}
	}
	return nil
}

// buildEnv merges extra env over the agent's own environment and forces PORT
// to the app port so apps that honour $PORT bind where vibeD expects.
func buildEnv(extra map[string]string, appPort int) []string {
	merged := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			merged[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range extra {
		merged[k] = v
	}
	merged["PORT"] = strconv.Itoa(appPort)

	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
