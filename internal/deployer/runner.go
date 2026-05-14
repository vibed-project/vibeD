package deployer

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vibed-project/vibeD/internal/pool"
	"github.com/vibed-project/vibeD/internal/runneragent"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/pkg/api"
)

// defaultConfirmWindow is how long Deploy watches a freshly injected process
// to make sure it stays up rather than crashing on startup.
const defaultConfirmWindow = 8 * time.Second

// agentClient is the subset of *runneragent.Client the deployer uses; an
// interface so tests can stub the HTTP boundary.
type agentClient interface {
	Inject(ctx context.Context, req runneragent.InjectRequest) (*runneragent.StatusResponse, error)
	Status(ctx context.Context) (*runneragent.StatusResponse, error)
	Logs(ctx context.Context, lines int) (*runneragent.LogsResponse, error)
}

// runnerPool is the subset of *pool.Pool the deployer needs; an interface so
// tests can stub the pool without standing up a fake Kubernetes client.
type runnerPool interface {
	Claim(ctx context.Context, language string) (*pool.Runner, error)
	Release(ctx context.Context, r *pool.Runner)
}

// RunnerDeployer is the Instant Preview fast path. Instead of deploying a
// pre-built image, it claims a warm runner pod from the pool and injects the
// artifact's source over the runner agent's control API — no per-request
// container build. The deployment is ephemeral: Delete recycles the runner.
type RunnerDeployer struct {
	pool          runnerPool
	storage       storage.Storage
	token         string
	logger        *slog.Logger
	newClient     func(controlURL, token string) agentClient
	confirmWindow time.Duration

	// runners maps artifact ID → its claimed runner. In-memory only: a vibeD
	// restart loses it, which is acceptable — previews are ephemeral, the pool
	// drains on shutdown, and a GC sweep reclaims anything a crash leaks.
	mu      sync.Mutex
	runners map[string]*pool.Runner
}

var _ Deployer = (*RunnerDeployer)(nil)

// NewRunnerDeployer creates a RunnerDeployer backed by the given warm pool.
func NewRunnerDeployer(p *pool.Pool, stg storage.Storage, token string, logger *slog.Logger) *RunnerDeployer {
	return &RunnerDeployer{
		pool:          p,
		storage:       stg,
		token:         token,
		logger:        logger,
		newClient:     func(controlURL, token string) agentClient { return runneragent.NewClient(controlURL, token) },
		confirmWindow: defaultConfirmWindow,
		runners:       make(map[string]*pool.Runner),
	}
}

// Deploy claims a warm runner, injects the artifact's source, and confirms the
// user process stays up. The runner is released back to the pool on failure so
// a botched deploy never leaks a pod.
func (d *RunnerDeployer) Deploy(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	// Read source before claiming so the warm runner isn't held idle during a
	// (potentially slow, for remote backends) source fetch.
	files, err := d.readSource(ctx, artifact)
	if err != nil {
		return nil, err
	}

	r, err := d.pool.Claim(ctx, artifact.Language)
	if err != nil {
		return nil, fmt.Errorf("claiming runner: %w", err)
	}

	if err := d.inject(ctx, r, artifact, files); err != nil {
		d.pool.Release(context.WithoutCancel(ctx), r)
		return nil, err
	}

	d.mu.Lock()
	d.runners[artifact.ID] = r
	d.mu.Unlock()

	d.logger.Info("runner deploy succeeded", "artifact", artifact.Name, "runner", r.Name, "url", r.AppURL())
	return &DeployResult{URL: r.AppURL()}, nil
}

// Update re-injects new source into the artifact's existing runner. If no
// runner is on record (e.g. vibeD restarted) it falls back to a fresh Deploy.
func (d *RunnerDeployer) Update(ctx context.Context, artifact *api.Artifact) (*DeployResult, error) {
	d.mu.Lock()
	r, ok := d.runners[artifact.ID]
	d.mu.Unlock()
	if !ok {
		d.logger.Info("no runner on record for update, redeploying", "artifact", artifact.Name)
		return d.Deploy(ctx, artifact)
	}

	files, err := d.readSource(ctx, artifact)
	if err != nil {
		return nil, err
	}
	if err := d.inject(ctx, r, artifact, files); err != nil {
		return nil, err
	}
	d.logger.Info("runner update succeeded", "artifact", artifact.Name, "runner", r.Name)
	return &DeployResult{URL: r.AppURL()}, nil
}

// Delete releases the artifact's runner back to the pool, which recycles it.
// A missing record is not an error — the pool drain or GC reclaims the pod.
func (d *RunnerDeployer) Delete(ctx context.Context, artifact *api.Artifact) error {
	d.mu.Lock()
	r, ok := d.runners[artifact.ID]
	delete(d.runners, artifact.ID)
	d.mu.Unlock()
	if !ok {
		d.logger.Debug("runner delete: no runner on record", "artifact", artifact.Name)
		return nil
	}
	d.pool.Release(ctx, r)
	d.logger.Info("runner deleted", "artifact", artifact.Name, "runner", r.Name)
	return nil
}

// GetURL returns the in-cluster app URL of the artifact's runner.
func (d *RunnerDeployer) GetURL(_ context.Context, artifact *api.Artifact) (string, error) {
	d.mu.Lock()
	r, ok := d.runners[artifact.ID]
	d.mu.Unlock()
	if ok {
		return r.AppURL(), nil
	}
	if artifact.URL != "" {
		return artifact.URL, nil
	}
	return "", fmt.Errorf("no runner on record for artifact %s", artifact.Name)
}

// GetLogs returns recent output of the artifact's user process, fetched from
// the runner agent.
func (d *RunnerDeployer) GetLogs(ctx context.Context, artifact *api.Artifact, lines int) ([]string, error) {
	d.mu.Lock()
	r, ok := d.runners[artifact.ID]
	d.mu.Unlock()
	if !ok {
		return nil, nil
	}
	resp, err := d.newClient(r.ControlURL(), d.token).Logs(ctx, lines)
	if err != nil {
		return nil, err
	}
	return resp.Lines, nil
}

// inject pushes source into a runner and confirms the user process starts and
// stays up through the confirmation window.
func (d *RunnerDeployer) inject(ctx context.Context, r *pool.Runner, artifact *api.Artifact, files map[string]string) error {
	client := d.newClient(r.ControlURL(), d.token)
	status, err := client.Inject(ctx, runneragent.InjectRequest{
		Language: artifact.Language,
		Files:    files,
		Env:      artifact.EnvVars,
		Port:     artifact.Port,
	})
	if err != nil {
		return fmt.Errorf("injecting source into runner %s: %w", r.Name, err)
	}
	if status.State == runneragent.StateFailed {
		return fmt.Errorf("runner %s failed to start user process: %s", r.Name, status.Error)
	}

	if err := d.confirmRunning(ctx, client); err != nil {
		return fmt.Errorf("runner %s user process did not stay up: %w%s", r.Name, err, tailLogs(ctx, client))
	}
	return nil
}

// confirmRunning polls the agent until the confirmation window elapses with the
// process still running, catching a process that starts then crashes.
func (d *RunnerDeployer) confirmRunning(ctx context.Context, client agentClient) error {
	deadline := time.Now().Add(d.confirmWindow)
	for {
		status, err := client.Status(ctx)
		if err != nil {
			return fmt.Errorf("checking runner status: %w", err)
		}
		switch status.State {
		case runneragent.StateFailed:
			return fmt.Errorf("process exited: %s", status.Error)
		case runneragent.StateExited:
			return fmt.Errorf("process exited before serving")
		}
		if time.Now().After(deadline) {
			return nil // still running after the window — good enough
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// readSource loads the artifact's stored source files into a path→content map
// for injection.
func (d *RunnerDeployer) readSource(ctx context.Context, artifact *api.Artifact) (map[string]string, error) {
	dir, err := d.storage.GetSourcePath(ctx, artifact.ID)
	if err != nil {
		return nil, fmt.Errorf("locating source for %s: %w", artifact.ID, err)
	}
	files := make(map[string]string)
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", rel, err)
		}
		files[rel] = string(content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading source for %s: %w", artifact.ID, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no source files found for artifact %s", artifact.ID)
	}
	return files, nil
}

// tailLogs best-effort fetches recent runner logs to enrich an error message.
func tailLogs(ctx context.Context, client agentClient) string {
	logs, err := client.Logs(ctx, 20)
	if err != nil || logs == nil || len(logs.Lines) == 0 {
		return ""
	}
	return "\n--- runner logs ---\n" + strings.Join(logs.Lines, "\n")
}
