package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
	"log/slog"
	"os"
)

const testNamespace = "default"

// Shared metrics instance to avoid duplicate registration panics.
var testMetrics = metrics.New()

func newTestGC(t *testing.T, clientset *fake.Clientset, st store.ArtifactStore, dryRun bool) *GarbageCollector {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	gc, err := NewGarbageCollector(
		clientset, st, testNamespace,
		config.GCConfig{
			Enabled:  true,
			Interval: "1h",
			MaxAge:   "1s", // Short maxAge so tests don't need to wait
			DryRun:   dryRun,
		},
		testMetrics, logger,
	)
	require.NoError(t, err)
	return gc
}

func completedJob(name, artifactID string, createdAt time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(createdAt),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/component":          "build",
				"vibed.dev/artifact-id":        artifactID,
			},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: "True"},
			},
		},
	}
}

func runningJob(name, artifactID string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/component":          "build",
				"vibed.dev/artifact-id":        artifactID,
			},
		},
	}
}

func testConfigMap(name, artifactID string, labels map[string]string) *corev1.ConfigMap {
	l := map[string]string{
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/artifact-id":        artifactID,
	}
	for k, v := range labels {
		l[k] = v
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    l,
		},
	}
}

func testDeployment(name, artifactID string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"vibed.dev/artifact-id":        artifactID,
			},
		},
	}
}

func testService(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
	}
}

func createArtifact(t *testing.T, st store.ArtifactStore, id, name string) {
	t.Helper()
	require.NoError(t, st.Create(context.Background(), &api.Artifact{
		ID:        id,
		Name:      name,
		Status:    api.StatusRunning,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}))
}

func TestGC_CleansOrphanedJobs(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		completedJob("vibed-build-x1", "artifact-x", time.Now().Add(-2*time.Hour)),
	)
	st := store.NewMemoryStore()
	// Don't create artifact "artifact-x" → it's orphaned.

	gc := newTestGC(t, clientset, st, false)
	// Wait for maxAge to pass.
	time.Sleep(2 * time.Millisecond)
	gc.collect(ctx)

	jobs, err := clientset.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items, "orphaned completed job should be deleted")
}

func TestGC_SkipsActiveJobs(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		runningJob("vibed-build-y1", "artifact-y"),
	)
	st := store.NewMemoryStore()

	gc := newTestGC(t, clientset, st, false)
	gc.collect(ctx)

	jobs, err := clientset.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "running job should NOT be deleted")
}

func TestGC_SkipsJobsWithinMaxAge(t *testing.T) {
	ctx := context.Background()
	// Job created just now — within maxAge even with 1s threshold.
	clientset := fake.NewSimpleClientset(
		completedJob("vibed-build-z1", "artifact-z", time.Now()),
	)
	st := store.NewMemoryStore()

	gc := newTestGC(t, clientset, st, false)
	gc.collect(ctx)

	jobs, err := clientset.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "recent job should NOT be deleted even if orphaned")
}

func TestGC_CleansOrphanedConfigMaps(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		testConfigMap("vibed-cm-x1", "artifact-x", nil),
	)
	st := store.NewMemoryStore()

	gc := newTestGC(t, clientset, st, false)
	gc.collect(ctx)

	cms, err := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, cms.Items, "orphaned configmap should be deleted")
}

func TestGC_SkipsStoreConfigMaps(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		testConfigMap("vibed-artifacts", "artifact-x", map[string]string{
			"app.kubernetes.io/component": "artifact-store",
		}),
	)
	st := store.NewMemoryStore()

	gc := newTestGC(t, clientset, st, false)
	gc.collect(ctx)

	cms, err := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, cms.Items, 1, "artifact-store configmap should NOT be deleted")
}

func TestGC_CleansOrphanedDeployments(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		testDeployment("my-app", "artifact-x"),
		testService("my-app"),
	)
	st := store.NewMemoryStore()

	gc := newTestGC(t, clientset, st, false)
	gc.collect(ctx)

	deploys, err := clientset.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, deploys.Items, "orphaned deployment should be deleted")

	svcs, err := clientset.CoreV1().Services(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, svcs.Items, "matching service should also be deleted")
}

func TestGC_DryRunDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		completedJob("vibed-build-x1", "artifact-x", time.Now().Add(-2*time.Hour)),
		testConfigMap("vibed-cm-x1", "artifact-x", nil),
		testDeployment("my-app", "artifact-x"),
	)
	st := store.NewMemoryStore()

	gc := newTestGC(t, clientset, st, true) // dryRun=true
	time.Sleep(2 * time.Millisecond)
	gc.collect(ctx)

	jobs, _ := clientset.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, jobs.Items, 1, "dry-run should NOT delete jobs")

	cms, _ := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, cms.Items, 1, "dry-run should NOT delete configmaps")

	deploys, _ := clientset.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, deploys.Items, 1, "dry-run should NOT delete deployments")
}

func TestGC_StopsOnContextCancel(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	st := store.NewMemoryStore()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	collector, err := NewGarbageCollector(
		clientset, st, testNamespace,
		config.GCConfig{
			Enabled:  true,
			Interval: "100ms",
			MaxAge:   "1s",
		},
		testMetrics, logger,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		collector.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// Run() returned as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("GC Run() did not return after context cancellation")
	}
}

func TestGC_KeepsResourcesWithActiveArtifact(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		testConfigMap("vibed-cm-a1", "artifact-a", nil),
		testDeployment("my-app", "artifact-a"),
	)
	st := store.NewMemoryStore()
	createArtifact(t, st, "artifact-a", "my-app")

	gc := newTestGC(t, clientset, st, false)
	gc.collect(ctx)

	cms, err := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, cms.Items, 1, "configmap with active artifact should NOT be deleted")

	deploys, err := clientset.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, deploys.Items, 1, "deployment with active artifact should NOT be deleted")
}
