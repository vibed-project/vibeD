//go:build integration

package deployer_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newK8sDeployer(t *testing.T, ns string) *deployer.KubernetesDeployer {
	t.Helper()
	clients := testutil.MustGetClients(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := config.DeploymentConfig{Namespace: ns}
	return deployer.NewKubernetesDeployer(clients.Clientset, cfg, logger)
}

func testArtifact(name string) *api.Artifact {
	return &api.Artifact{
		ID:       "test-" + name,
		Name:     name,
		ImageRef: testutil.TestImage,
		Port:     80,
		Target:   api.TargetKubernetes,
	}
}

func TestKubernetesDeployer_Deploy(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newK8sDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	result, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, result.URL)

	// Verify Deployment was created
	dep, err := clients.Clientset.AppsV1().Deployments(ns).Get(ctx, artifact.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, artifact.Name, dep.Name)
	assert.Equal(t, testutil.TestImage, dep.Spec.Template.Spec.Containers[0].Image)

	// Verify Service was created
	svc, err := clients.Clientset.CoreV1().Services(ns).Get(ctx, artifact.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, artifact.Name, svc.Name)

	// Wait for pod to be ready
	testutil.WaitForDeploymentReady(t, clients.Clientset, ns, artifact.Name, 90*time.Second)
}

func TestKubernetesDeployer_GetURL(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newK8sDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	_, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)

	url, err := d.GetURL(ctx, artifact)
	require.NoError(t, err)
	assert.Contains(t, url, "http://")
}

func TestKubernetesDeployer_Update(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newK8sDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	_, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)

	// Update with an env var
	artifact.EnvVars = map[string]string{"NEW_VAR": "new_value"}
	result, err := d.Update(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, result.URL)

	// Verify the Deployment was updated
	dep, err := clients.Clientset.AppsV1().Deployments(ns).Get(ctx, artifact.Name, metav1.GetOptions{})
	require.NoError(t, err)

	foundEnv := false
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "NEW_VAR" && env.Value == "new_value" {
			foundEnv = true
			break
		}
	}
	assert.True(t, foundEnv, "updated env var should be present in Deployment")
}

func TestKubernetesDeployer_GetLogs(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newK8sDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	_, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)

	// Wait for pod to be running
	testutil.WaitForPodRunning(t, clients.Clientset, ns, "app="+artifact.Name, 90*time.Second)
	// Give the container a moment to produce logs
	time.Sleep(2 * time.Second)

	logs, err := d.GetLogs(ctx, artifact, 10)
	require.NoError(t, err)
	// nginx produces access/error log lines on startup
	assert.NotEmpty(t, logs, "should have at least some log output")
}

func TestKubernetesDeployer_Delete(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newK8sDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	_, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)

	err = d.Delete(ctx, artifact)
	require.NoError(t, err)

	// Verify Deployment is gone
	_, err = clients.Clientset.AppsV1().Deployments(ns).Get(ctx, artifact.Name, metav1.GetOptions{})
	assert.Error(t, err, "Deployment should be deleted")

	// Verify Service is gone
	_, err = clients.Clientset.CoreV1().Services(ns).Get(ctx, artifact.Name, metav1.GetOptions{})
	assert.Error(t, err, "Service should be deleted")
}

func TestKubernetesDeployer_FullLifecycle(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newK8sDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())

	// Deploy
	result, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, result.URL)

	// Wait for ready
	testutil.WaitForDeploymentReady(t, clients.Clientset, ns, artifact.Name, 90*time.Second)

	// GetURL
	url, err := d.GetURL(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, url)

	// GetLogs
	testutil.WaitForPodRunning(t, clients.Clientset, ns, "app="+artifact.Name, 30*time.Second)
	time.Sleep(2 * time.Second)
	logs, err := d.GetLogs(ctx, artifact, 10)
	require.NoError(t, err)
	assert.NotNil(t, logs)

	// Update
	artifact.EnvVars = map[string]string{"UPDATED": "true"}
	result, err = d.Update(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, result.URL)

	// Delete
	err = d.Delete(ctx, artifact)
	require.NoError(t, err)
}
