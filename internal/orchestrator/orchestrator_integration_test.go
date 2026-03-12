//go:build integration

package orchestrator_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testOrch creates a fully wired Orchestrator for integration testing.
// It uses a mock builder, real K8s deployer, local storage, and in-memory store.
func testOrch(t *testing.T) (*orchestrator.Orchestrator, string) {
	t.Helper()

	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := testutil.TestConfig(ns, tmpDir)

	// Storage
	localStorage, err := storage.NewLocalStorage(tmpDir)
	require.NoError(t, err)

	// Store (in-memory for simplicity)
	memStore := store.NewMemoryStore()

	// Builder (mock)
	mockBuilder := &testutil.MockBuilder{}

	// Deployer factory with K8s deployer
	factory := deployer.NewFactory()
	k8sDep := deployer.NewKubernetesDeployer(
		clients.Clientset,
		cfg.Deployment,
		logger,
	)
	factory.Register(api.TargetKubernetes, k8sDep)

	// Environment detector
	detector := environment.NewDetector(clients, logger)

	// Metrics
	m := metrics.New()

	orch := orchestrator.NewOrchestrator(
		cfg,
		detector,
		mockBuilder,
		factory,
		localStorage,
		memStore,
		m,
		logger,
	)

	return orch, ns
}

func TestOrchestrator_DeployAndStatus(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	req := testutil.SampleDeployRequest(testutil.RandomName())
	result, err := orch.Deploy(ctx, req)
	require.NoError(t, err)

	assert.NotEmpty(t, result.ArtifactID)
	assert.Equal(t, req.Name, result.Name)
	assert.NotEmpty(t, result.URL)
	assert.Equal(t, "kubernetes", result.Target)
	assert.Equal(t, "running", result.Status)

	// Check Status
	artifact, err := orch.Status(ctx, result.ArtifactID)
	require.NoError(t, err)
	assert.Equal(t, api.StatusRunning, artifact.Status)
	assert.Equal(t, req.Name, artifact.Name)
	assert.NotEmpty(t, artifact.URL)
	assert.NotEmpty(t, artifact.ImageRef)
}

func TestOrchestrator_DeployAndList(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	req1 := testutil.SampleDeployRequest(testutil.RandomName())
	req2 := testutil.SampleDeployRequest(testutil.RandomName())

	_, err := orch.Deploy(ctx, req1)
	require.NoError(t, err)
	_, err = orch.Deploy(ctx, req2)
	require.NoError(t, err)

	list, err := orch.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestOrchestrator_DeployListFilter(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	req := testutil.SampleDeployRequest(testutil.RandomName())
	_, err := orch.Deploy(ctx, req)
	require.NoError(t, err)

	running, err := orch.List(ctx, "running")
	require.NoError(t, err)
	assert.Len(t, running, 1)

	failed, err := orch.List(ctx, "failed")
	require.NoError(t, err)
	assert.Len(t, failed, 0)
}

func TestOrchestrator_UpdateArtifact(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	req := testutil.SampleDeployRequest(testutil.RandomName())
	deployResult, err := orch.Deploy(ctx, req)
	require.NoError(t, err)

	// Update with new files and env vars
	updateReq := orchestrator.UpdateRequest{
		ArtifactID: deployResult.ArtifactID,
		Files: map[string]string{
			"index.html": "<html><body>Updated!</body></html>",
		},
		EnvVars: map[string]string{"VERSION": "2"},
	}

	updateResult, err := orch.Update(ctx, updateReq)
	require.NoError(t, err)
	assert.Equal(t, "running", updateResult.Status)
	assert.NotEmpty(t, updateResult.URL)

	// Verify status
	artifact, err := orch.Status(ctx, deployResult.ArtifactID)
	require.NoError(t, err)
	assert.Equal(t, api.StatusRunning, artifact.Status)
}

func TestOrchestrator_DeleteArtifact(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	req := testutil.SampleDeployRequest(testutil.RandomName())
	deployResult, err := orch.Deploy(ctx, req)
	require.NoError(t, err)

	err = orch.Delete(ctx, deployResult.ArtifactID)
	require.NoError(t, err)

	// Verify artifact is gone
	_, err = orch.Status(ctx, deployResult.ArtifactID)
	require.Error(t, err)
	assert.IsType(t, &api.ErrNotFound{}, err)
}

func TestOrchestrator_FullLifecycle(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	name := testutil.RandomName()
	req := testutil.SampleDeployRequest(name)

	// 1. Deploy
	deployResult, err := orch.Deploy(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "running", deployResult.Status)
	artifactID := deployResult.ArtifactID

	// 2. List — should contain the artifact
	list, err := orch.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, name, list[0].Name)

	// 3. Status — should be running
	artifact, err := orch.Status(ctx, artifactID)
	require.NoError(t, err)
	assert.Equal(t, api.StatusRunning, artifact.Status)

	// 4. Update
	updateResult, err := orch.Update(ctx, orchestrator.UpdateRequest{
		ArtifactID: artifactID,
		Files:      map[string]string{"index.html": "<html>v2</html>"},
	})
	require.NoError(t, err)
	assert.Equal(t, "running", updateResult.Status)

	// 5. Delete
	err = orch.Delete(ctx, artifactID)
	require.NoError(t, err)

	// 6. List — should be empty
	list, err = orch.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, list, 0)
}

func TestOrchestrator_ListTargets(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)

	targets := orch.ListTargets()
	assert.NotEmpty(t, targets)

	// Kubernetes should always be available
	var k8sFound bool
	for _, target := range targets {
		if target.Name == api.TargetKubernetes {
			k8sFound = true
			assert.True(t, target.Available)
		}
	}
	assert.True(t, k8sFound, "Kubernetes target should be listed")
}

func TestOrchestrator_InvalidInput(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	// Empty name
	_, err := orch.Deploy(ctx, orchestrator.DeployRequest{
		Name:   "",
		Files:  testutil.SampleHTMLFiles(),
		Target: "kubernetes",
	})
	require.Error(t, err)
	assert.IsType(t, &api.ErrInvalidInput{}, err)

	// Empty files
	_, err = orch.Deploy(ctx, orchestrator.DeployRequest{
		Name:   testutil.RandomName(),
		Files:  map[string]string{},
		Target: "kubernetes",
	})
	require.Error(t, err)
	assert.IsType(t, &api.ErrInvalidInput{}, err)
}

func TestOrchestrator_DuplicateName(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, _ := testOrch(t)
	ctx := context.Background()

	name := testutil.RandomName()
	req := testutil.SampleDeployRequest(name)

	_, err := orch.Deploy(ctx, req)
	require.NoError(t, err)

	// Deploy again with same name
	_, err = orch.Deploy(ctx, req)
	require.Error(t, err)
	assert.IsType(t, &api.ErrAlreadyExists{}, err)
}

func TestOrchestrator_BuildFailure(t *testing.T) {
	testutil.SkipIfNoCluster(t)

	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := testutil.TestConfig(ns, tmpDir)
	localStorage, err := storage.NewLocalStorage(tmpDir)
	require.NoError(t, err)
	memStore := store.NewMemoryStore()

	// Mock builder that always fails
	failBuilder := &testutil.MockBuilder{
		Err: errors.New("buildpack failed: out of memory"),
	}

	factory := deployer.NewFactory()
	k8sDep := deployer.NewKubernetesDeployer(clients.Clientset, cfg.Deployment, logger)
	factory.Register(api.TargetKubernetes, k8sDep)

	detector := environment.NewDetector(clients, logger)
	m := metrics.New()

	orch := orchestrator.NewOrchestrator(cfg, detector, failBuilder, factory, localStorage, memStore, m, logger)

	ctx := context.Background()
	req := testutil.SampleDeployRequest(testutil.RandomName())

	_, err = orch.Deploy(ctx, req)
	require.Error(t, err)
	assert.IsType(t, &api.ErrBuildFailed{}, err)

	// Verify the artifact is marked as failed in the store
	list, err := orch.List(ctx, "failed")
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestOrchestrator_Logs(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	orch, ns := testOrch(t)
	ctx := context.Background()
	clients := testutil.MustGetClients(t)

	req := testutil.SampleDeployRequest(testutil.RandomName())
	deployResult, err := orch.Deploy(ctx, req)
	require.NoError(t, err)

	// Wait for pod to be running
	testutil.WaitForPodRunning(t, clients.Clientset, ns, "app="+req.Name, 90*time.Second)
	time.Sleep(2 * time.Second)

	logs, err := orch.Logs(ctx, deployResult.ArtifactID, 10)
	require.NoError(t, err)
	assert.NotNil(t, logs)
}
