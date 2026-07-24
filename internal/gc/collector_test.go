package gc

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
	"log/slog"
	"os"
)

const (
	testAppsNamespace = "vibed-apps"
	// labelAppKey is the live label internal/controller stamps on every
	// SandboxClaim (claim.go: appLabelKey); value = the owning VibedApp name.
	labelAppKey = "vibed.dev/app"
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

// sandboxClaim builds an Unstructured SandboxClaim CR (as the /v1 path creates
// it in claim.go): named in the apps namespace and carrying the vibed.dev/app
// label whose value is its owning VibedApp's name.
func sandboxClaim(name, app string, createdAt time.Time) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   sandboxClaimGVR.Group,
		Version: sandboxClaimGVR.Version,
		Kind:    "SandboxClaim",
	})
	u.SetName(name)
	u.SetNamespace(testAppsNamespace)
	u.SetCreationTimestamp(metav1.NewTime(createdAt))
	u.SetLabels(map[string]string{labelAppKey: app})
	return u
}

// dynClientWithClaims builds a fake dynamic client seeded with the given
// SandboxClaim CRs and the GVR->ListKind mapping the sweep needs to List.
func dynClientWithClaims(claims ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	objs := make([]runtime.Object, 0, len(claims))
	for _, c := range claims {
		objs = append(objs, c)
	}
	gvrToListKind := map[schema.GroupVersionResource]string{
		sandboxClaimGVR: "SandboxClaimList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
}

// listClaims returns the SandboxClaim names still present in the apps namespace.
func listClaims(t *testing.T, ctx context.Context, dyn dynamic.Interface) []string {
	t.Helper()
	list, err := dyn.Resource(sandboxClaimGVR).Namespace(testAppsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	names := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		names = append(names, it.GetName())
	}
	return names
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

func newTestGC(t *testing.T, dyn dynamic.Interface, ctrlClient ctrlclient.Client, dryRun bool) *GarbageCollector {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	gc, err := NewGarbageCollector(
		dyn, ctrlClient, testAppsNamespace,
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

func TestGC_ListActiveApps_BuildsSetFromCRNamesAndPages(t *testing.T) {
	ctx := context.Background()
	names := []string{"app-a", "app-b", "app-c", "app-d", "app-e"}
	client := &pagedAppClient{apps: names, pageSize: 2}

	gc := newTestGC(t, nil, client, false)
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

// TestGC_ReapsOrphanedClaim (a): a claim whose owning VibedApp CR is absent AND
// that is older than MaxAge is reaped — the owner-ref cascade backstop.
func TestGC_ReapsOrphanedClaim(t *testing.T) {
	ctx := context.Background()
	dyn := dynClientWithClaims(
		sandboxClaim("claim-gone", "gone", time.Now().Add(-2*time.Hour)),
	)
	// No VibedApp CR named "gone" → its claim is orphaned.
	gc := newTestGC(t, dyn, ctrlClientWithApps(t), false)
	time.Sleep(2 * time.Millisecond) // let MaxAge (1s, but timestamp is 2h old) pass trivially
	gc.collect(ctx)

	assert.Empty(t, listClaims(t, ctx, dyn), "orphaned claim past MaxAge should be reaped")
}

// TestGC_SparesLiveAppClaim (b): a claim whose owning VibedApp is in the active
// set is SPARED — the GC never reaps a live app's claim.
func TestGC_SparesLiveAppClaim(t *testing.T) {
	ctx := context.Background()
	dyn := dynClientWithClaims(
		sandboxClaim("claim-live", "app-live", time.Now().Add(-2*time.Hour)),
	)
	// A VibedApp CR named "app-live" exists → its claim is live.
	gc := newTestGC(t, dyn, ctrlClientWithApps(t, "app-live"), false)
	gc.collect(ctx)

	assert.Equal(t, []string{"claim-live"}, listClaims(t, ctx, dyn),
		"a live app's claim must never be reaped")
}

// TestGC_SparesYoungOrphanClaim (c): a claim younger than MaxAge is spared even
// when its VibedApp is gone — a delete racing an in-flight create would be wrong.
func TestGC_SparesYoungOrphanClaim(t *testing.T) {
	ctx := context.Background()
	dyn := dynClientWithClaims(
		sandboxClaim("claim-old", "gone", time.Now().Add(-2*time.Hour)),
		sandboxClaim("claim-young", "gone", time.Now()),
	)
	gc := newTestGC(t, dyn, ctrlClientWithApps(t), false) // no live apps
	gc.collect(ctx)

	assert.Equal(t, []string{"claim-young"}, listClaims(t, ctx, dyn),
		"young orphan should be spared, old one reaped")
}

// TestGC_ListErrorFailsSafe (d): when the VibedApp list fails, the GC cycle
// makes no deletions (fail-safe) rather than treating the live set as empty and
// reaping everything.
func TestGC_ListErrorFailsSafe(t *testing.T) {
	ctx := context.Background()
	dyn := dynClientWithClaims(
		sandboxClaim("claim-gone", "gone", time.Now().Add(-2*time.Hour)),
	)
	client := &pagedAppClient{listErr: fmt.Errorf("apiserver unavailable")}
	gc := newTestGC(t, dyn, client, false)
	gc.collect(ctx)

	assert.Equal(t, []string{"claim-gone"}, listClaims(t, ctx, dyn),
		"list error must fail safe: no deletions")
}

// TestGC_DryRunDoesNotDelete (e): with DryRun the sweep logs candidates but
// deletes nothing.
func TestGC_DryRunDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	dyn := dynClientWithClaims(
		sandboxClaim("claim-gone", "gone", time.Now().Add(-2*time.Hour)),
	)
	gc := newTestGC(t, dyn, ctrlClientWithApps(t), true) // dryRun=true
	gc.collect(ctx)

	assert.Equal(t, []string{"claim-gone"}, listClaims(t, ctx, dyn),
		"dry-run should not delete the orphaned claim")
}

// TestGC_ReapsManyOrphanClaimsConcurrently exercises the bounded-concurrency
// delete path with far more orphans than the worker-pool limit: every orphan
// must still be deleted (nothing dropped or double-processed), and the run must
// be race-clean (go test -race).
func TestGC_ReapsManyOrphanClaimsConcurrently(t *testing.T) {
	ctx := context.Background()
	const n = 50 // > gcDeleteConcurrency
	claims := make([]*unstructured.Unstructured, 0, n)
	for i := 0; i < n; i++ {
		claims = append(claims, sandboxClaim(fmt.Sprintf("claim-%d", i), fmt.Sprintf("gone-%d", i), time.Now().Add(-2*time.Hour)))
	}
	dyn := dynClientWithClaims(claims...)
	gc := newTestGC(t, dyn, ctrlClientWithApps(t), false) // no active apps → all orphaned
	gc.collect(ctx)

	assert.Empty(t, listClaims(t, ctx, dyn), "all %d orphaned claims should be reaped", n)
}

// TestGC_NoDynamicClientSkipsSweep asserts the sweep is a no-op (no panic) when
// the dynamic client is nil — the cluster has no agent-sandbox CRDs.
func TestGC_NoDynamicClientSkipsSweep(t *testing.T) {
	ctx := context.Background()
	gc := newTestGC(t, nil, ctrlClientWithApps(t), false)
	gc.collect(ctx) // must not panic
}

func TestGC_StopsOnContextCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	collector, err := NewGarbageCollector(
		dynClientWithClaims(), ctrlClientWithApps(t), testAppsNamespace,
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
