//go:build integration

package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/k8s"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stubRunnerDeployer satisfies the Deployer interface for the promote tests.
// The real RunnerDeployer needs a warm pool + agent-sandbox CRD; doPromote only
// calls Delete on it (to release the pooled runner once the built deployment is
// up), so a stub that records the call is enough to exercise the full promote
// pipeline against the real KubernetesDeployer.
type stubRunnerDeployer struct {
	deleteCalls atomic.Int32
}

func (s *stubRunnerDeployer) Deploy(_ context.Context, _ *api.Artifact) (*deployer.DeployResult, error) {
	return &deployer.DeployResult{URL: "http://stub-runner.svc.cluster.local:8080"}, nil
}
func (s *stubRunnerDeployer) Update(_ context.Context, _ *api.Artifact) (*deployer.DeployResult, error) {
	return &deployer.DeployResult{URL: "http://stub-runner.svc.cluster.local:8080"}, nil
}
func (s *stubRunnerDeployer) Delete(_ context.Context, _ *api.Artifact) error {
	s.deleteCalls.Add(1)
	return nil
}
func (s *stubRunnerDeployer) GetURL(_ context.Context, a *api.Artifact) (string, error) {
	return a.URL, nil
}
func (s *stubRunnerDeployer) GetLogs(_ context.Context, _ *api.Artifact, _ int) ([]string, error) {
	return nil, nil
}

// promoteRig wires an orchestrator the same way testOrch does, but registers
// the stub RunnerDeployer alongside the real KubernetesDeployer so the test
// can exercise the promote pipeline. It also exposes the store/storage/builder
// so the test can stage a fast-path-style preview artifact directly.
type promoteRig struct {
	orch       *orchestrator.Orchestrator
	ns         string
	store      *store.SQLiteStore
	storage    storage.Storage
	builder    *testutil.MockBuilder
	runnerStub *stubRunnerDeployer
	clients    *k8s.Clients
}

func newPromoteRig(t *testing.T) *promoteRig {
	t.Helper()
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := testutil.TestConfig(ns, tmpDir)

	localStorage, err := storage.NewLocalStorage(tmpDir)
	require.NoError(t, err)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	sqliteStore, err := store.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { sqliteStore.Close() })

	// nginx-unprivileged runs as UID 101 and listens on 8080 — required because
	// the KubernetesDeployer enforces runAsNonRoot:true on every pod.
	mockBuilder := &testutil.MockBuilder{ImageRef: "nginxinc/nginx-unprivileged:alpine"}

	factory := deployer.NewFactory()
	factory.Register(api.TargetKubernetes, deployer.NewKubernetesDeployer(clients.Clientset, 0, logger))
	runnerStub := &stubRunnerDeployer{}
	factory.Register(api.TargetRunner, runnerStub)

	detector := environment.NewDetector(clients, logger)
	m := metrics.New()
	bus := events.NewEventBus()

	orch := orchestrator.NewOrchestrator(
		cfg, detector, mockBuilder, factory,
		localStorage, sqliteStore, sqliteStore, m,
		clients.Clientset, bus, sqliteStore, logger,
	)
	orch.SetLifecycleContext(context.Background())

	return &promoteRig{
		orch: orch, ns: ns, store: sqliteStore, storage: localStorage,
		builder: mockBuilder, runnerStub: runnerStub, clients: clients,
	}
}

// stagePreview creates a preview artifact in the store, with source files
// staged on disk, mimicking what the orchestrator would produce after a
// successful runner deploy. Returns the artifact ID for promote.
func (r *promoteRig) stagePreview(t *testing.T, ctx context.Context) *api.Artifact {
	t.Helper()
	id := "preview-" + testutil.RandomName()
	files := map[string]string{
		"index.html": "<!doctype html><title>preview</title><h1>preview</h1>",
	}
	ref, err := r.storage.StoreSource(ctx, id, files)
	require.NoError(t, err)

	artifact := &api.Artifact{
		ID:         id,
		Name:       testutil.RandomName(),
		Namespace:  r.ns,
		Status:     api.StatusRunning,
		Target:     api.TargetRunner,
		Mode:       api.ModePreview,
		URL:        "http://stub-runner.svc.cluster.local:8080",
		Language:   "static",
		Port:       8080, // matches the MockBuilder image (nginxinc/nginx-unprivileged)
		StorageRef: ref.LocalPath,
		Version:    1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	require.NoError(t, r.store.Create(ctx, artifact))
	return artifact
}

func TestOrchestrator_PromotePreview(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	rig := newPromoteRig(t)
	ctx := context.Background()

	preview := rig.stagePreview(t, ctx)

	// Trigger the promote — returns immediately with status "building".
	result, err := rig.orch.AsyncPromote(ctx, preview.ID)
	require.NoError(t, err)
	assert.Equal(t, "building", result.Status)

	// Wait for the swap to land.
	testutil.WaitForCondition(t, 90*time.Second, 2*time.Second, func() bool {
		a, err := rig.orch.Status(ctx, preview.ID)
		if err != nil {
			return false
		}
		return a.Mode == api.ModeBuilt && a.Status == api.StatusRunning
	}, "preview promoted to mode=built / status=running")

	// Final state.
	a, err := rig.orch.Status(ctx, preview.ID)
	require.NoError(t, err)
	assert.Equal(t, api.ModeBuilt, a.Mode, "Mode should flip to built")
	assert.Equal(t, api.TargetKubernetes, a.Target, "Target should swap to the durable backend")
	assert.NotEmpty(t, a.ImageRef, "ImageRef should be set from the build")
	assert.Equal(t, preview.ID, a.ID, "artifact ID is stable across promote")
	assert.NotEqual(t, preview.URL, a.URL, "URL changes — runner address → built deployment URL")

	// The build ran, and the runner was released.
	assert.GreaterOrEqual(t, rig.builder.BuildCalls(), 1, "MockBuilder should have been called")
	assert.Equal(t, int32(1), rig.runnerStub.deleteCalls.Load(), "runner deployer.Delete should have been called once")

	// The K8s deployment is real.
	_, err = rig.clients.Clientset.AppsV1().Deployments(rig.ns).Get(ctx, a.Name, metav1.GetOptions{})
	require.NoError(t, err, "K8s Deployment should exist after promote")
}

func TestOrchestrator_PromoteRejectsNonPreview(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	rig := newPromoteRig(t)
	ctx := context.Background()

	// A normal built artifact, not a preview.
	id := "built-" + testutil.RandomName()
	artifact := &api.Artifact{
		ID: id, Name: testutil.RandomName(), Namespace: rig.ns,
		Status: api.StatusRunning, Target: api.TargetKubernetes, Mode: api.ModeBuilt,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, rig.store.Create(ctx, artifact))

	_, err := rig.orch.AsyncPromote(ctx, id)
	require.Error(t, err, "promoting a built artifact must error synchronously")
	var invErr *api.ErrInvalidInput
	assert.True(t, errors.As(err, &invErr), "error type should be *api.ErrInvalidInput, got %T", err)
}

// TestOrchestrator_PromoteConcurrent fires N promotes in parallel and waits
// for all of them to swap to mode=built / status=running. The aim is to catch
// concurrency bugs in doPromote (deadlocks, store-update races, metric races,
// double-release of the same runner) — not to benchmark the warm pool itself,
// which needs the real fast-path stack on a live cluster.
func TestOrchestrator_PromoteConcurrent(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	rig := newPromoteRig(t)
	ctx := context.Background()

	const n = 5
	previews := make([]*api.Artifact, n)
	for i := 0; i < n; i++ {
		previews[i] = rig.stagePreview(t, ctx)
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := time.Now()
	for _, p := range previews {
		wg.Add(1)
		go func(p *api.Artifact) {
			defer wg.Done()
			if _, err := rig.orch.AsyncPromote(ctx, p.ID); err != nil {
				errs <- fmt.Errorf("AsyncPromote(%s): %w", p.ID, err)
			}
		}(p)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// All N artifacts must reach built/running. Generous timeout because each
	// promote spins up a real K8s Deployment + Service.
	testutil.WaitForCondition(t, 3*time.Minute, 2*time.Second, func() bool {
		for _, p := range previews {
			a, err := rig.orch.Status(ctx, p.ID)
			if err != nil || a.Mode != api.ModeBuilt || a.Status != api.StatusRunning {
				return false
			}
		}
		return true
	}, fmt.Sprintf("all %d previews promoted to mode=built / status=running", n))

	elapsed := time.Since(start)
	t.Logf("promoted %d previews concurrently in %s (avg %s/promote)", n, elapsed.Round(time.Millisecond), (elapsed / n).Round(time.Millisecond))

	assert.GreaterOrEqual(t, rig.builder.BuildCalls(), n, "MockBuilder should have been called at least once per promote")
	assert.Equal(t, int32(n), rig.runnerStub.deleteCalls.Load(), "each promote should release its runner exactly once")

	// Per-artifact final state.
	for _, p := range previews {
		a, err := rig.orch.Status(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, api.ModeBuilt, a.Mode, "%s should be built", p.ID)
		assert.Equal(t, api.TargetKubernetes, a.Target, "%s should be on kubernetes", p.ID)
		assert.NotEmpty(t, a.URL, "%s should have a URL", p.ID)
	}
}

func TestOrchestrator_PromoteFailureRestoresPreview(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	rig := newPromoteRig(t)
	ctx := context.Background()

	preview := rig.stagePreview(t, ctx)
	// Force the build to fail; the promote pipeline must restore the preview
	// state verbatim and leave the artifact running.
	rig.builder.Err = errors.New("simulated build failure")

	_, err := rig.orch.AsyncPromote(ctx, preview.ID)
	require.NoError(t, err)

	// Give the background goroutine time to run the build, fail, and restore.
	testutil.WaitForCondition(t, 30*time.Second, time.Second, func() bool {
		a, err := rig.orch.Status(ctx, preview.ID)
		if err != nil {
			return false
		}
		// We're done once the artifact has been restored to its preview state.
		return a.Status == api.StatusRunning && a.Mode == api.ModePreview && a.Target == api.TargetRunner
	}, "preview restored to running/preview/runner after build failure")

	a, err := rig.orch.Status(ctx, preview.ID)
	require.NoError(t, err)
	assert.Equal(t, api.ModePreview, a.Mode, "Mode should be restored to preview")
	assert.Equal(t, api.TargetRunner, a.Target, "Target should be restored to runner")
	assert.Equal(t, api.StatusRunning, a.Status, "Status should be restored to running")
	assert.Empty(t, a.Error, "no error should be persisted on the artifact")
	assert.Equal(t, int32(0), rig.runnerStub.deleteCalls.Load(), "runner must NOT be released on a failed promote")
}
