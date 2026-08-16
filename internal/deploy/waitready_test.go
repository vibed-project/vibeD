package deploy

// waitReady's watch path: with a Watcher configured, readiness is observed
// from a watch event stream (one initial Get + events) instead of re-Getting
// the CR every PollInterval. Every test here sets PollInterval to an hour so
// any accidental dependence on polling blows straight past the deadline.

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// newWatchService builds a Service over a watch-capable fake client with a
// poll interval so long that only the watch path can meet the deadline.
func newWatchService(t *testing.T) (*Service, client.WithWatch) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.Watcher = c
	svc.DeployTimeout = 5 * time.Second
	svc.PollInterval = time.Hour
	return svc, c
}

func createApp(t *testing.T, c client.Client, name string, phase vibedv1.Phase) {
	t.Helper()
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: "alice@example.com"},
	}
	if err := c.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if phase != "" {
		setPhase(t, c, name, phase)
	}
}

func setPhase(t *testing.T, c client.Client, name string, phase vibedv1.Phase) {
	t.Helper()
	app := &vibedv1.VibedApp{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "vibed-apps"}, app); err != nil {
		t.Fatalf("get app: %v", err)
	}
	app.Status.Phase = phase
	if phase == vibedv1.PhaseReady {
		app.Status.URL = "https://abc123def456.vibed.example.com"
	}
	if err := c.Status().Update(context.Background(), app); err != nil {
		t.Fatalf("status update: %v", err)
	}
}

// markPhase flips the app's phase after a delay, best-effort like markReady,
// so it is safe to run off the test goroutine (t.Fatal is not).
func markPhase(c client.Client, name string, after time.Duration, phase vibedv1.Phase) {
	go func() {
		time.Sleep(after)
		app := &vibedv1.VibedApp{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "vibed-apps"}, app); err != nil {
			return
		}
		app.Status.Phase = phase
		if phase == vibedv1.PhaseReady {
			app.Status.URL = "https://abc123def456.vibed.example.com"
		}
		_ = c.Status().Update(context.Background(), app)
	}()
}

// TestWaitReadyWatchReadyAfterUpdates drives two status transitions
// (Claiming, then Ready) and asserts waitReady returns on the Ready event —
// well before the deadline and without any PollInterval sleeps.
func TestWaitReadyWatchReadyAfterUpdates(t *testing.T) {
	svc, c := newWatchService(t)
	createApp(t, c, "hello", "")

	markPhase(c, "hello", 20*time.Millisecond, vibedv1.PhaseClaiming)
	markPhase(c, "hello", 40*time.Millisecond, vibedv1.PhaseReady)

	start := time.Now()
	res, err := svc.waitReady(context.Background(), "vibed-apps", "hello")
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if !res.Ready || res.Phase != vibedv1.PhaseReady {
		t.Fatalf("expected Ready, got %+v", res)
	}
	if res.URL == "" {
		t.Error("expected URL on Ready")
	}
	// The Ready event lands ~40ms in; anything near PollInterval (1h) or the
	// 5s deadline means the watch path didn't fire.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waitReady took %v; expected event-driven return", elapsed)
	}
}

// TestWaitReadyWatchAlreadyReady covers the initial-Get catch: a plain watch
// does not replay existing objects, so an app that went Ready before
// waitReady started must be observed by the Get, immediately.
func TestWaitReadyWatchAlreadyReady(t *testing.T) {
	svc, c := newWatchService(t)
	createApp(t, c, "already", vibedv1.PhaseReady)

	start := time.Now()
	res, err := svc.waitReady(context.Background(), "vibed-apps", "already")
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if !res.Ready || res.Phase != vibedv1.PhaseReady || res.URL == "" {
		t.Fatalf("expected immediate Ready, got %+v", res)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitReady took %v; expected immediate return", elapsed)
	}
}

// TestWaitReadyWatchFailed maps a Failed event to the same not-ready Result
// the poll loop returns.
func TestWaitReadyWatchFailed(t *testing.T) {
	svc, c := newWatchService(t)
	createApp(t, c, "broken", "")

	markPhase(c, "broken", 20*time.Millisecond, vibedv1.PhaseFailed)

	res, err := svc.waitReady(context.Background(), "vibed-apps", "broken")
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if res.Ready || res.Phase != vibedv1.PhaseFailed {
		t.Fatalf("expected Failed not-ready, got %+v", res)
	}
	if res.AppID != "broken" {
		t.Errorf("app id = %q", res.AppID)
	}
}

// TestWaitReadyWatchTimeoutPending: never Ready → the 202-pending Result at
// the deadline, not an error.
func TestWaitReadyWatchTimeoutPending(t *testing.T) {
	svc, c := newWatchService(t)
	svc.DeployTimeout = 80 * time.Millisecond
	createApp(t, c, "slow", vibedv1.PhaseClaiming)

	res, err := svc.waitReady(context.Background(), "vibed-apps", "slow")
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if res.Ready {
		t.Error("expected not-ready on timeout")
	}
	if res.AppID != "slow" || res.Phase != vibedv1.PhaseClaiming {
		t.Errorf("pending result = %+v, want app slow in phase Claiming", res)
	}
}

// TestWaitReadyWatchContextCancel preserves the poll loop's cancellation
// semantics: a cancelled ctx surfaces ctx.Err(), not a pending Result.
func TestWaitReadyWatchContextCancel(t *testing.T) {
	svc, c := newWatchService(t)
	createApp(t, c, "cancelled", "")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	if _, err := svc.waitReady(ctx, "vibed-apps", "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// failingWatcher is a WithWatch whose Watch always errors, forcing the
// poll fallback.
type failingWatcher struct {
	client.WithWatch
}

func (f failingWatcher) Watch(context.Context, client.ObjectList, ...client.ListOption) (watch.Interface, error) {
	return nil, errors.New("watch verb forbidden")
}

// TestWaitReadyWatchSetupErrorFallsBackToPoll: a watch that can't be
// established (e.g. RBAC without the watch verb) degrades to the poll loop
// rather than failing the deploy.
func TestWaitReadyWatchSetupErrorFallsBackToPoll(t *testing.T) {
	svc, c := newWatchService(t)
	svc.Watcher = failingWatcher{WithWatch: c}
	svc.PollInterval = 10 * time.Millisecond
	createApp(t, c, "fallback", "")

	markPhase(c, "fallback", 30*time.Millisecond, vibedv1.PhaseReady)

	res, err := svc.waitReady(context.Background(), "vibed-apps", "fallback")
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if !res.Ready || res.Phase != vibedv1.PhaseReady {
		t.Fatalf("expected Ready via poll fallback, got %+v", res)
	}
}

// TestDeployWatchReachesReady runs the full Deploy path with a Watcher and an
// hour-long PollInterval: only the watch can observe the controller's Ready
// flip inside the deadline.
func TestDeployWatchReachesReady(t *testing.T) {
	svc, c := newWatchService(t)
	markReady(c, "hello", "vibed-apps", 30*time.Millisecond)

	res, err := svc.Deploy(context.Background(), Request{
		Name:    "hello",
		Owner:   "alice@example.com",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "<h1>hi</h1>"})),
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !res.Ready || res.Phase != vibedv1.PhaseReady {
		t.Fatalf("expected Ready, got %+v", res)
	}
}
