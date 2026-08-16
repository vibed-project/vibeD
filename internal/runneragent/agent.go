package runneragent

import (
	"context"
	"crypto/subtle"
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
	// RequireAuth makes the control API (inject/status/logs/stop) fail closed
	// when no Token is configured, instead of serving those endpoints
	// unauthenticated. Set via VIBED_AGENT_REQUIRE_AUTH=true. It does not affect
	// the unauthenticated liveness (/healthz) and image-info (/info) probes.
	RequireAuth bool
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

	// Language is the image's declared template language (VIBED_TEMPLATE_LANGUAGE),
	// reported verbatim by GET /info for bring-your-own base-image validation.
	Language string
	// RuntimeProbe is a command (VIBED_RUNTIME_PROBE), e.g. "node --version",
	// that GET /info runs once to prove the language runtime is present.
	RuntimeProbe string
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

	// Fetcher pulls SourceURL-based inject payloads. Pluggable so tests can
	// swap in an in-process httptest server.
	Fetcher *SourceFetcher

	mu       sync.Mutex
	gen      uint64        // bumped on every inject; lets stale supervisors no-op
	cmd      *exec.Cmd     // current user process, nil when idle
	done     chan struct{} // closed by supervise() once cmd.Wait() has returned
	state    string
	exitCode *int
	command  []string
	appPort  int
	lastErr  string
	// stages holds the base names of in-flight staging dirs living inside the
	// workdir. resetWorkdirLocked must not clear them (that would delete a
	// pending inject's fetched source); anything stage-shaped NOT in this set
	// is stale residue from a crashed agent and is removed like any entry.
	stages map[string]struct{}

	infoOnce sync.Once    // the runtime probe runs at most once
	infoResp InfoResponse // cached /info result
}

// New returns a ready-to-Run Agent.
func New(cfg Config) *Agent {
	cfg.applyDefaults()
	return &Agent{
		cfg:     cfg,
		logs:    newRingBuffer(cfg.LogLines),
		state:   StateIdle,
		stages:  map[string]struct{}{},
		Fetcher: NewSourceFetcher(),
	}
}

// handler builds the control-API HTTP handler. Exposed separately from Run so
// tests can drive it via httptest without binding a port.
func (a *Agent) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealthz, a.handleHealthz) // unauthenticated liveness probe
	mux.HandleFunc(PathInfo, a.handleInfo)       // unauthenticated image metadata (BYO validation)
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

// auth wraps a control-API handler with Bearer-token authentication.
//
// When a Token is configured it is required (constant-time compared). When no
// Token is configured the behavior depends on RequireAuth: if set, the endpoint
// fails closed (401) so a misconfiguration cannot silently expose the control
// API to the untrusted workload sharing the pod; if unset, the endpoint is
// served without auth (the historical single-node/dev default).
func (a *Agent) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.Token == "" {
			if a.cfg.RequireAuth {
				writeError(w, http.StatusUnauthorized, "control API requires authentication but no token is configured")
				return
			}
			next(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.cfg.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
			return
		}
		next(w, r)
	}
}

func (a *Agent) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (a *Agent) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.info())
}

// info reports the image's declared language and runs the runtime probe once
// (cached) to confirm the language runtime is actually present. Used by
// vibeD's bring-your-own base-image validation.
func (a *Agent) info() InfoResponse {
	a.infoOnce.Do(func() {
		resp := InfoResponse{
			Language:      a.cfg.Language,
			AgentContract: AgentContract,
			RuntimeProbe:  a.cfg.RuntimeProbe,
		}
		if probe := strings.TrimSpace(a.cfg.RuntimeProbe); probe != "" {
			// Run via `sh -c` so images can use compound probes (e.g. the
			// kitchen-sink base checking node + python + go). All vibeD images
			// have a shell; the entrypoint contract already requires one.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "sh", "-c", probe).CombinedOutput()
			resp.RuntimeProbeOutput = strings.TrimSpace(string(out))
			resp.RuntimeProbeOK = err == nil
		}
		a.infoResp = resp
	})
	return a.infoResp
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
	hasFiles := len(req.Files) > 0
	hasURL := req.SourceURL != ""
	switch {
	case !hasFiles && !hasURL:
		writeError(w, http.StatusBadRequest, "either files or source_url is required")
		return
	case hasFiles && hasURL:
		writeError(w, http.StatusBadRequest, "files and source_url are mutually exclusive")
		return
	}

	status, err := a.inject(r.Context(), req)
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

// stagePattern is the os.MkdirTemp pattern for in-workdir staging dirs. The
// dot prefix keeps a briefly-visible staging dir out of the user process's
// way; the random suffix makes concurrent stages collision-free.
const stagePattern = ".vibed-stage-*"

// inject writes the source (inline files or tarball pulled from SourceURL)
// and (re)starts the user process. Any process from a previous inject is
// stopped first.
//
// We don't hold a.mu across the tarball fetch — that's a network round-trip
// and would block /status and /healthz for the duration. Source is staged
// into a hidden temp directory inside the workdir first, then swapped into
// place under the lock.
func (a *Agent) inject(ctx context.Context, req InjectRequest) (StatusResponse, error) {
	// Resolve the run command. When SourceURL is used we don't know the file
	// list at this point, so for the autodetect path the caller must pass
	// Command explicitly (or set Language so RunCommand returns a fixed
	// language-default). This keeps the agent off the file-system before
	// taking the lock.
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

	// Tarball pull happens outside the lock. The data lands in a fresh staging
	// dir INSIDE the workdir — same filesystem as the final destination — so
	// the swap below is an O(1) rename instead of a full re-copy (/tmp and the
	// workspace are usually separate mounts in a sandbox pod, which would force
	// moveDirContents onto its EXDEV copy path for the whole tree). A partial
	// fetch still never corrupts the workdir: the staged tree is only adopted
	// after a successful fetch and is removed on every other path.
	var stagedDir string
	if req.SourceURL != "" {
		if err := os.MkdirAll(a.cfg.Workdir, 0o755); err != nil {
			return StatusResponse{}, fmt.Errorf("staging dir: %w", err)
		}
		dir, err := os.MkdirTemp(a.cfg.Workdir, stagePattern)
		if err != nil {
			return StatusResponse{}, fmt.Errorf("staging dir: %w", err)
		}
		// Register the staging dir so resetWorkdirLocked — ours below, or a
		// concurrent inject's — doesn't clear it out from under the fetch.
		// Unregister and remove it on the way out, success or failure.
		name := filepath.Base(dir)
		a.mu.Lock()
		a.stages[name] = struct{}{}
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			delete(a.stages, name)
			a.mu.Unlock()
			_ = os.RemoveAll(dir)
		}()
		fetcher := a.Fetcher
		if fetcher == nil {
			fetcher = NewSourceFetcher()
		}
		if err := fetcher.Fetch(ctx, req.SourceURL, dir); err != nil {
			return StatusResponse{}, err
		}
		stagedDir = dir
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Replace any existing process.
	a.stopProcessLocked()

	if err := a.resetWorkdirLocked(); err != nil {
		return StatusResponse{}, err
	}
	if stagedDir != "" {
		if err := moveDirContents(stagedDir, a.cfg.Workdir); err != nil {
			return StatusResponse{}, err
		}
	} else if err := writeFiles(a.cfg.Workdir, req.Files); err != nil {
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

// moveDirContents moves every entry under src into dst. dst is expected to
// hold nothing but in-flight staging dirs (resetWorkdirLocked guarantees this
// for the inject path). src itself sits inside dst, so its entries land on
// the same filesystem and os.Rename is the common, O(1) path; src's own
// entries are moved, never src itself, so the staging dir is not moved into
// itself. The recursive-copy fallback stays as defense for exotic setups
// where an entry still crosses a mount and rename fails with EXDEV
// ("invalid cross-device link").
func moveDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("staged dir: %w", err)
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if err := os.Rename(from, to); err == nil {
			continue
		} else if !errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("move %s → %s: %w", from, to, err)
		}
		// Cross-device: copy then remove the source.
		if err := copyPath(from, to); err != nil {
			return fmt.Errorf("copy %s → %s: %w", from, to, err)
		}
		_ = os.RemoveAll(from)
	}
	return nil
}

// copyPath recursively copies src to dst (files, dirs; symlinks/specials are
// skipped, matching the tar-extraction allowlist).
func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil // skip symlinks/devices/etc.
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
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

// hardKillGrace bounds the wait for a process to be reaped after SIGKILL, so a
// descendant that escaped the process group (and thus kept a log pipe open)
// cannot wedge the agent's control API by holding a.mu indefinitely.
const hardKillGrace = 5 * time.Second

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
		// Bound this wait too. done closes only after cmd.Wait() returns, and
		// cmd.Wait() blocks until the stdout/stderr copy goroutine sees EOF —
		// which needs EVERY write end closed, including one inherited by a
		// descendant that escaped the process group (so SIGKILL never reached
		// it). An unbounded receive here would hold a.mu forever and wedge the
		// whole control API. Abandoning the reap leaks at most one goroutine;
		// deadlocking the agent takes down the sandbox.
		select {
		case <-done:
		case <-time.After(hardKillGrace):
			a.cfg.Logger.Error("user process did not reap after SIGKILL; abandoning",
				"pid", pid, "grace", hardKillGrace)
		}
	}
	a.cfg.Logger.Info("user process stopped", "pid", pid)
}

// resetWorkdirLocked empties the workdir without removing the directory itself
// (it may be a mounted volume). In-flight staging dirs (a.stages) are kept:
// clearing one would delete a pending inject's fetched source. Stage-shaped
// dirs NOT in the set are crash leftovers and are removed like any entry.
// Caller holds a.mu.
func (a *Agent) resetWorkdirLocked() error {
	entries, err := os.ReadDir(a.cfg.Workdir)
	if err != nil {
		return fmt.Errorf("reading workdir: %w", err)
	}
	for _, e := range entries {
		if _, inFlight := a.stages[e.Name()]; inFlight {
			continue
		}
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
// isAgentPrivateEnv reports whether an env var belongs to the agent's own
// control plane and must never reach the user process.
//
// The user process is UNTRUSTED code sharing this container, and it inherits
// the agent's environment. VIBED_AGENT_TOKEN is the bearer token for the
// control API on :9000 — leaking it lets the workload call /inject on itself
// and, since vibeD issues the token per sandbox-template rather than per app,
// on other sandboxes it can reach, turning any deployed app into arbitrary code
// execution in its neighbours. The whole VIBED_ prefix is dropped (deny by
// default) so a future control variable can't silently re-open this; anything a
// user app legitimately needs arrives via `extra` (the deploy's own env), which
// is applied after this filter and can therefore still set VIBED_-prefixed
// values deliberately.
func isAgentPrivateEnv(key string) bool {
	return strings.HasPrefix(key, "VIBED_")
}

func buildEnv(extra map[string]string, appPort int) []string {
	merged := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		if isAgentPrivateEnv(kv[:i]) {
			continue
		}
		merged[kv[:i]] = kv[i+1:]
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
