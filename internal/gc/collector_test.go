package gc

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
	"log/slog"
	"os"
)

const (
	testNamespace     = "default"
	testAppsNamespace = "vibed-apps"
)

// Shared metrics instance to avoid duplicate registration panics.
var testMetrics = metrics.New()

// vibedScheme is a runtime.Scheme with the VibedApp CRD types registered.
func vibedScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, vibedv1.AddToScheme(scheme))
	return scheme
}

// ctrlClientWithApps builds a fake controller-runtime client seeded with the
// given VibedApp CRs (in the apps namespace).
func ctrlClientWithApps(t *testing.T, names ...string) ctrlclient.Client {
	t.Helper()
	b := ctrlfake.NewClientBuilder().WithScheme(vibedScheme(t))
	for _, n := range names {
		b = b.WithObjects(vibedApp(n))
	}
	return b.Build()
}

func vibedApp(name string) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testAppsNamespace,
		},
	}
}

// pagedAppClient is a minimal client.Client that serves a fixed VibedApp set
// with real Limit/Continue paging (the controller-runtime fake client ignores
// Limit), so the collector's Continue loop is genuinely exercised. It uses a
// small fixed page size regardless of the requested Limit and counts List
// calls; listErr forces the fail-safe path.
type pagedAppClient struct {
	ctrlclient.Client // embedded for the methods we never call
	apps              []string
	pageSize          int
	listErr           error
	listCalls         int
}

func (p *pagedAppClient) List(_ context.Context, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
	p.listCalls++
	if p.listErr != nil {
		return p.listErr
	}
	appList, ok := list.(*vibedv1.VibedAppList)
	if !ok {
		return fmt.Errorf("pagedAppClient: unexpected list type %T", list)
	}
	lo := ctrlclient.ListOptions{}
	lo.ApplyOptions(opts)

	start := 0
	if lo.Continue != "" {
		start, _ = strconv.Atoi(lo.Continue)
	}
	end := len(p.apps)
	cont := ""
	if p.pageSize > 0 && start+p.pageSize < len(p.apps) {
		end = start + p.pageSize
		cont = strconv.Itoa(end)
	}
	appList.Items = nil
	for _, n := range p.apps[start:end] {
		appList.Items = append(appList.Items, *vibedApp(n))
	}
	appList.Continue = cont
	return nil
}

func newTestGC(t *testing.T, clientset *fake.Clientset, ctrlClient ctrlclient.Client, legacy, dryRun bool) *GarbageCollector {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	gc, err := NewGarbageCollector(
		clientset, nil, ctrlClient, testNamespace, testAppsNamespace,
		config.GCConfig{
			Enabled:      true,
			Interval:     "1h",
			MaxAge:       "1s", // Short maxAge so tests don't need to wait
			DryRun:       dryRun,
			LegacySweeps: legacy,
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
	return testConfigMapAt(name, artifactID, time.Time{}, labels)
}

// testConfigMapAt builds a legacy-labelled ConfigMap with an explicit creation
// time so the MaxAge guard can be exercised. A zero createdAt is far in the
// past (older than any maxAge), matching the earlier no-timestamp fixtures.
func testConfigMapAt(name, artifactID string, createdAt time.Time, labels map[string]string) *corev1.ConfigMap {
	l := map[string]string{
		"app.kubernetes.io/managed-by": "vibed",
		"vibed.dev/artifact-id":        artifactID,
	}
	for k, v := range labels {
		l[k] = v
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(createdAt),
			Labels:            l,
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

func TestGC_ListActiveApps_BuildsSetFromCRNamesAndPages(t *testing.T) {
	ctx := context.Background()
	names := []string{"app-a", "app-b", "app-c", "app-d", "app-e"}
	client := &pagedAppClient{apps: names, pageSize: 2}

	gc := newTestGC(t, fake.NewSimpleClientset(), client, true, false)
	active, err := gc.listActiveApps(ctx)
	require.NoError(t, err)

	// Active set is keyed by CR name.
	require.Len(t, active, len(names))
	for _, n := range names {
		assert.True(t, active[n], "expected %q in active set", n)
	}
	assert.False(t, active["not-an-app"])

	// 5 apps at page size 2 => pages [2,2,1] => 3 List calls; the Continue loop
	// must have followed the token twice.
	assert.Equal(t, 3, client.listCalls, "expected the Continue loop to page through all apps")
}

func TestGC_CleansOrphanedJobs(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		completedJob("vibed-build-x1", "artifact-x", time.Now().Add(-2*time.Hour)),
	)
	// No VibedApp CR named "artifact-x" → it's orphaned.
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false)
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
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false)
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
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false)
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
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false)
	gc.collect(ctx)

	cms, err := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, cms.Items, "orphaned configmap should be deleted")
}

// TestGC_CleansManyOrphansConcurrently exercises the bounded-concurrency delete
// path (#76) with far more orphans than the worker-pool limit: every orphan must
// still be deleted (nothing dropped or double-processed), and the run must be
// race-clean (go test -race).
func TestGC_CleansManyOrphansConcurrently(t *testing.T) {
	ctx := context.Background()
	const n = 50 // > gcDeleteConcurrency
	objs := make([]runtime.Object, 0, n)
	for i := 0; i < n; i++ {
		objs = append(objs, testConfigMap(fmt.Sprintf("vibed-cm-%d", i), fmt.Sprintf("gone-%d", i), nil))
	}
	clientset := fake.NewSimpleClientset(objs...)
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false) // no active apps → all orphaned
	gc.collect(ctx)

	cms, err := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, cms.Items, "all %d orphaned configmaps should be deleted", n)
}

func TestGC_SkipsStoreConfigMaps(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		testConfigMap("vibed-artifacts", "artifact-x", map[string]string{
			"app.kubernetes.io/component": "artifact-store",
		}),
	)
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false)
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
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false)
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
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, true) // dryRun=true
	time.Sleep(2 * time.Millisecond)
	gc.collect(ctx)

	jobs, _ := clientset.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, jobs.Items, 1, "dry-run should NOT delete jobs")

	cms, _ := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, cms.Items, 1, "dry-run should NOT delete configmaps")

	deploys, _ := clientset.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, deploys.Items, 1, "dry-run should NOT delete deployments")
}

// TestGC_LegacySweepsDisabledSkips asserts that with LegacySweeps=false, no
// legacy-labelled resource is touched even when it's a clear orphan past maxAge.
func TestGC_LegacySweepsDisabledSkips(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		completedJob("vibed-build-x1", "artifact-x", time.Now().Add(-2*time.Hour)),
		testConfigMap("vibed-cm-x1", "artifact-x", nil),
		testDeployment("my-app", "artifact-x"),
	)
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), false, false) // legacy=false
	time.Sleep(2 * time.Millisecond)
	gc.collect(ctx)

	jobs, _ := clientset.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, jobs.Items, 1, "legacy sweeps disabled: job should NOT be deleted")
	cms, _ := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, cms.Items, 1, "legacy sweeps disabled: configmap should NOT be deleted")
	deploys, _ := clientset.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{})
	assert.Len(t, deploys.Items, 1, "legacy sweeps disabled: deployment should NOT be deleted")
}

// TestGC_ReapsOldSparesYoungOrphan asserts the MaxAge guard on the legacy
// sweeps: an orphan (artifact-id absent from the live CR set) older than maxAge
// is reaped, but one younger than maxAge is spared.
func TestGC_ReapsOldSparesYoungOrphan(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		testConfigMapAt("cm-old", "gone", time.Now().Add(-2*time.Hour), nil),
		testConfigMapAt("cm-young", "gone", time.Now(), nil),
	)
	gc := newTestGC(t, clientset, ctrlClientWithApps(t), true, false) // no live CRs
	time.Sleep(2 * time.Millisecond)
	gc.collect(ctx)

	cms, err := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, cms.Items, 1, "only the young orphan should survive")
	assert.Equal(t, "cm-young", cms.Items[0].Name, "young orphan should be spared, old one reaped")
}

// TestGC_ListErrorFailsSafe asserts that when the VibedApp list fails, the GC
// cycle makes no deletions (fail-safe) rather than treating the live set as
// empty and reaping everything.
func TestGC_ListErrorFailsSafe(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		completedJob("vibed-build-x1", "artifact-x", time.Now().Add(-2*time.Hour)),
	)
	client := &pagedAppClient{listErr: fmt.Errorf("apiserver unavailable")}
	gc := newTestGC(t, clientset, client, true, false)
	time.Sleep(2 * time.Millisecond)
	gc.collect(ctx)

	jobs, err := clientset.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "list error must fail safe: no deletions")
}

func TestGC_StopsOnContextCancel(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	collector, err := NewGarbageCollector(
		clientset, nil, ctrlClientWithApps(t), testNamespace, testAppsNamespace,
		config.GCConfig{
			Enabled:      true,
			Interval:     "100ms",
			MaxAge:       "1s",
			LegacySweeps: true,
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

// TestGC_KeepsResourcesWithLiveApp asserts a legacy-labelled resource whose
// artifact-id matches a live VibedApp CR name is spared (the CR set is the
// authoritative live set).
func TestGC_KeepsResourcesWithLiveApp(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(
		testConfigMap("vibed-cm-a1", "artifact-a", nil),
		testDeployment("my-app", "artifact-a"),
	)
	// A VibedApp CR named "artifact-a" exists → its resources are live.
	gc := newTestGC(t, clientset, ctrlClientWithApps(t, "artifact-a"), true, false)
	gc.collect(ctx)

	cms, err := clientset.CoreV1().ConfigMaps(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, cms.Items, 1, "configmap for a live app should NOT be deleted")

	deploys, err := clientset.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, deploys.Items, 1, "deployment for a live app should NOT be deleted")
}
