//go:build integration

package environment_test

import (
	"log/slog"
	"os"
	"testing"

	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/k8s"
	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestDetector(t *testing.T) *environment.Detector {
	t.Helper()
	clients := testutil.MustGetClients(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return environment.NewDetector(clients, logger)
}

func TestDetector_DetectsKubernetes(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	d := newTestDetector(t)

	result := d.Detect()
	assert.True(t, result.Kubernetes, "Kubernetes should always be detected as available")
}

func TestDetector_DetectsKnative(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	clients := testutil.MustGetClients(t)

	// Check if Knative CRDs are installed
	hasKnative, err := k8s.HasCRD(clients.Discovery, "serving.knative.dev", "v1", "services")
	require.NoError(t, err)

	if !hasKnative {
		t.Skip("skipping: Knative CRDs not installed in cluster")
	}

	d := newTestDetector(t)
	result := d.Detect()
	assert.True(t, result.Knative, "Knative should be detected when CRDs are installed")
}

func TestDetector_SelectTarget_Auto(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	d := newTestDetector(t)

	target, err := d.SelectTarget(api.TargetAuto)
	require.NoError(t, err)

	// Auto should select either Knative (if installed) or Kubernetes
	assert.Contains(t, []api.DeploymentTarget{api.TargetKnative, api.TargetKubernetes}, target)
}

func TestDetector_SelectTarget_Explicit(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	d := newTestDetector(t)

	target, err := d.SelectTarget(api.TargetKubernetes)
	require.NoError(t, err)
	assert.Equal(t, api.TargetKubernetes, target)
}

func TestDetector_ListTargets(t *testing.T) {
	testutil.SkipIfNoCluster(t)
	d := newTestDetector(t)

	targets := d.ListTargets()
	assert.Len(t, targets, 2, "should return 2 target types")

	// Find each target
	var k8sTarget, knativeTarget *api.TargetInfo
	for i := range targets {
		switch targets[i].Name {
		case api.TargetKubernetes:
			k8sTarget = &targets[i]
		case api.TargetKnative:
			knativeTarget = &targets[i]
		}
	}

	require.NotNil(t, k8sTarget, "Kubernetes target should be present")
	assert.True(t, k8sTarget.Available, "Kubernetes should be available")

	require.NotNil(t, knativeTarget, "Knative target should be present")
	// Knative availability depends on cluster setup
}
