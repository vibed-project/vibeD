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
	"github.com/vibed-project/vibeD/internal/k8s"
	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knversioned "knative.dev/serving/pkg/client/clientset/versioned"
)

func skipIfNoKnative(t *testing.T) {
	t.Helper()
	clients := testutil.MustGetClients(t)
	hasKnative, err := k8s.HasCRD(clients.Discovery, "serving.knative.dev", "v1", "services")
	if err != nil || !hasKnative {
		t.Skip("skipping: Knative CRDs not installed in cluster")
	}
}

func newKnativeDeployer(t *testing.T, ns string) *deployer.KnativeDeployer {
	t.Helper()
	clients := testutil.MustGetClients(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	knClient, err := knversioned.NewForConfig(clients.RestConfig)
	require.NoError(t, err)

	depCfg := config.DeploymentConfig{Namespace: ns}
	knCfg := config.KnativeConfig{
		DomainSuffix: "127.0.0.1.sslip.io",
		IngressClass: "kourier.ingress.networking.knative.dev",
	}

	return deployer.NewKnativeDeployer(knClient, clients.Clientset, depCfg, knCfg, logger)
}

func TestKnativeDeployer_Deploy(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	skipIfNoKnative(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newKnativeDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	artifact.Target = api.TargetKnative

	result, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, result.URL, "Knative Service should return a URL")
	assert.Contains(t, result.URL, "sslip.io", "URL should contain the domain suffix")
}

func TestKnativeDeployer_Update(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	skipIfNoKnative(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newKnativeDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	artifact.Target = api.TargetKnative

	_, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)

	// Update with new env var
	artifact.EnvVars = map[string]string{"UPDATED": "true"}
	result, err := d.Update(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, result.URL)
}

func TestKnativeDeployer_Delete(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	skipIfNoKnative(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newKnativeDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	artifact.Target = api.TargetKnative

	_, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)

	err = d.Delete(ctx, artifact)
	require.NoError(t, err)
}

func TestKnativeDeployer_FullLifecycle(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	skipIfNoKnative(t)
	clients := testutil.MustGetClients(t)
	ns := testutil.CreateTestNamespace(t, clients.Clientset)

	d := newKnativeDeployer(t, ns)
	ctx := context.Background()

	artifact := testArtifact(testutil.RandomName())
	artifact.Target = api.TargetKnative

	// Deploy
	result, err := d.Deploy(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, result.URL)

	// GetURL
	url, err := d.GetURL(ctx, artifact)
	require.NoError(t, err)
	assert.NotEmpty(t, url)

	// Wait a bit for pods to start before getting logs
	time.Sleep(5 * time.Second)

	// GetLogs — may return empty if pods haven't started yet
	logs, err := d.GetLogs(ctx, artifact, 10)
	// Logs may fail if no pods are running yet (scale-to-zero), that's OK
	if err == nil {
		assert.NotNil(t, logs)
	}

	// Delete
	err = d.Delete(ctx, artifact)
	require.NoError(t, err)
}
