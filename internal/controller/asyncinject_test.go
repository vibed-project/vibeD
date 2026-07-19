package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// startingApp seeds an app mid-state-machine at PhaseStarting with a bound
// pod, the point where injection happens.
func startingApp(name string) *vibedv1.VibedApp {
	app := validApp(name)
	app.Status = vibedv1.VibedAppStatus{
		Phase:      vibedv1.PhaseStarting,
		SandboxRef: "sb-" + name,
		PodIP:      "10.0.0.42",
	}
	return app
}

// reconcileUntil reconciles repeatedly — the requeue loop a real controller
// would drive — until cond holds or the deadline passes.
func reconcileUntil(t *testing.T, r *Reconciler, app *vibedv1.VibedApp, what string, cond func(*vibedv1.VibedApp) bool) *vibedv1.VibedApp {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := runOnce(t, r, app)
		if cond(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; status=%+v", what, got.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// latencyInjector is a fake Injector with controllable latency and result.
type latencyInjector struct {
	latency time.Duration
	running bool
	err     error

	mu    sync.Mutex
	calls int
}

func (l *latencyInjector) Inject(ctx context.Context, _ string, _ *vibedv1.VibedApp) (bool, error) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	if l.latency > 0 {
		select {
		case <-time.After(l.latency):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return l.running, l.err
}

func (l *latencyInjector) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// gatedInjector blocks every call until release is closed, so tests control
// exactly when an in-flight inject completes.
type gatedInjector struct {
	release chan struct{}
	running bool
	err     error

	mu    sync.Mutex
	calls int
}

func (g *gatedInjector) Inject(ctx context.Context, _ string, _ *vibedv1.VibedApp) (bool, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	g.mu.Lock()
	running, err := g.running, g.err
	g.mu.Unlock()
	return running, err
}

func (g *gatedInjector) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// TestInjectDoesNotParkReconcile: with an injector that takes 2s, Reconcile
// must return in a small fraction of that — the whole point of the async
// design is that a slow /inject never parks a reconcile worker. While the
// inject is in flight the app stays in Starting with Ready=False/Injecting
// and a requeue drives the next look.
func TestInjectDoesNotParkReconcile(t *testing.T) {
	app := startingApp("slow-inject")
	inj := &latencyInjector{latency: 2 * time.Second, running: true}
	r := newReconciler(t, app, func(r *Reconciler) {
		r.Injector = inj
		r.InjectGrace = 10 * time.Millisecond
	})

	begin := time.Now()
	got, res := runOnce(t, r, app)
	if wall := time.Since(begin); wall > time.Second {
		t.Fatalf("Reconcile took %v with a 2s inject in flight; must return promptly", wall)
	}
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Errorf("phase=%q want Starting while inject is in flight", got.Status.Phase)
	}
	if !hasConditionWithReason(got, ConditionReady, metav1.ConditionFalse, ReasonInjecting) {
		t.Errorf("expected Ready=False/Injecting while in flight, got %+v", got.Status.Conditions)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue to observe the in-flight inject")
	}
}

// TestAsyncInjectSuccessMatchesSyncSurface: once an asynchronous inject
// completes successfully, the app must surface exactly what the old
// synchronous path did on success (the TestHappyPath spec): PhaseReady, URL
// and LastDeployedAt set, Ready=True/Running.
func TestAsyncInjectSuccessMatchesSyncSurface(t *testing.T) {
	app := startingApp("async-ok")
	inj := &gatedInjector{release: make(chan struct{}), running: true}
	r := newReconciler(t, app, func(r *Reconciler) {
		r.Injector = inj
		r.InjectGrace = time.Millisecond
	})

	got, res := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Fatalf("phase=%q want Starting while inject is in flight", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue while inject is in flight")
	}

	close(inj.release)
	got = reconcileUntil(t, r, app, "PhaseReady", func(a *vibedv1.VibedApp) bool {
		return a.Status.Phase == vibedv1.PhaseReady
	})
	if got.Status.URL == "" {
		t.Error("expected URL to be set when Ready")
	}
	if got.Status.LastDeployedAt == nil {
		t.Error("expected LastDeployedAt to be set when Ready")
	}
	if !hasConditionWithReason(got, ConditionReady, metav1.ConditionTrue, ReasonRunning) {
		t.Errorf("expected Ready=True/Running, got %+v", got.Status.Conditions)
	}
	if inj.callCount() != 1 {
		t.Errorf("inject called %d times, want 1 (success consumed once)", inj.callCount())
	}
}

// TestInjectErrorSurfacesInline: a fast-failing inject completes within the
// grace window and must surface exactly what the synchronous path surfaced on
// error, in the same single pass: stay Starting, Ready=False/InjectFailed
// carrying the error message, and a requeue.
func TestInjectErrorSurfacesInline(t *testing.T) {
	app := startingApp("inject-err")
	inj := &latencyInjector{err: errors.New("tarball fetch: 403 Forbidden")}
	r := newReconciler(t, app, func(r *Reconciler) { r.Injector = inj })

	got, res := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Errorf("phase=%q want Starting on inject error (retryable)", got.Status.Phase)
	}
	if !hasConditionWithReason(got, ConditionReady, metav1.ConditionFalse, ReasonInjectFailed) {
		t.Fatalf("expected Ready=False/InjectFailed, got %+v", got.Status.Conditions)
	}
	if cond := findCondition(got, ConditionReady); !strings.Contains(cond.Message, "403 Forbidden") {
		t.Errorf("condition message %q must carry the inject error", cond.Message)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected RequeueAfter on inject error")
	}
}

// TestAsyncInjectErrorSurfacesAndRetries: an inject that fails after going
// async surfaces the same InjectFailed condition as the sync path did, and —
// exactly like the sync path's requeue — the next Starting pass launches a
// fresh inject attempt.
func TestAsyncInjectErrorSurfacesAndRetries(t *testing.T) {
	app := startingApp("async-err")
	inj := &gatedInjector{release: make(chan struct{}), err: errors.New("agent exploded")}
	r := newReconciler(t, app, func(r *Reconciler) {
		r.Injector = inj
		r.InjectGrace = time.Millisecond
	})

	got, _ := runOnce(t, r, app) // launches attempt 1
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Fatalf("phase=%q want Starting while inject is in flight", got.Status.Phase)
	}

	close(inj.release)
	got = reconcileUntil(t, r, app, "InjectFailed condition", func(a *vibedv1.VibedApp) bool {
		return hasConditionWithReason(a, ConditionReady, metav1.ConditionFalse, ReasonInjectFailed)
	})
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Errorf("phase=%q want Starting on inject error (retryable, matches sync path)", got.Status.Phase)
	}
	if cond := findCondition(got, ConditionReady); !strings.Contains(cond.Message, "agent exploded") {
		t.Errorf("condition message %q must carry the inject error", cond.Message)
	}

	// The error was consumed; the next pass must retry with a fresh inject
	// (release is already closed, so attempt 2 succeeds immediately).
	inj.mu.Lock()
	inj.err = nil
	inj.running = true
	inj.mu.Unlock()
	got = reconcileUntil(t, r, app, "PhaseReady after retry", func(a *vibedv1.VibedApp) bool {
		return a.Status.Phase == vibedv1.PhaseReady
	})
	if inj.callCount() < 2 {
		t.Errorf("inject called %d times, want >=2 (error must trigger a fresh attempt)", inj.callCount())
	}
}

// redeployInjector blocks its first call until cancelled/released and records
// the tarball ref of every call, so the redeploy-mid-flight test can prove the
// stale attempt was discarded and the fresh one delivered the new source.
type redeployInjector struct {
	block chan struct{}
	first chan struct{}

	mu      sync.Mutex
	sources []string
}

func (ri *redeployInjector) Inject(ctx context.Context, _ string, app *vibedv1.VibedApp) (bool, error) {
	ri.mu.Lock()
	n := len(ri.sources) + 1
	ri.sources = append(ri.sources, app.Spec.Source.TarballRef)
	ri.mu.Unlock()
	if n == 1 {
		close(ri.first)
		select {
		case <-ri.block:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return true, nil
}

// TestRedeployMidFlightDiscardsStaleInject: a redeploy (new tarball ref) while
// an inject is in flight must cancel the stale attempt, discard its result,
// and start a fresh inject of the NEW source — the app must reach Ready via
// the new tarball, never observing the stale attempt's outcome.
func TestRedeployMidFlightDiscardsStaleInject(t *testing.T) {
	app := startingApp("redeploy")
	inj := &redeployInjector{block: make(chan struct{}), first: make(chan struct{})}
	r := newReconciler(t, app, func(r *Reconciler) {
		r.Injector = inj
		r.InjectGrace = time.Millisecond
	})

	got, _ := runOnce(t, r, app) // launches the v1 inject
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Fatalf("phase=%q want Starting while v1 inject is in flight", got.Status.Phase)
	}
	select {
	case <-inj.first:
	case <-time.After(5 * time.Second):
		t.Fatal("v1 inject never started")
	}

	// Redeploy: bump the tarball while v1 is still in flight.
	live := &vibedv1.VibedApp{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Source.TarballRef = "s3://bucket/redeploy-v2.tar.gz"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}

	// The token changed, so the next Starting pass cancels v1 (its blocked
	// call unblocks via ctx cancellation and errors — that stale error must
	// never surface) and injects v2, which succeeds immediately.
	got = reconcileUntil(t, r, app, "PhaseReady via v2", func(a *vibedv1.VibedApp) bool {
		return a.Status.Phase == vibedv1.PhaseReady
	})
	if !hasConditionWithReason(got, ConditionReady, metav1.ConditionTrue, ReasonRunning) {
		t.Errorf("expected Ready=True/Running (stale cancelled result must be discarded), got %+v", got.Status.Conditions)
	}
	inj.mu.Lock()
	sources := append([]string(nil), inj.sources...)
	inj.mu.Unlock()
	if len(sources) != 2 {
		t.Fatalf("inject calls=%d (%v), want exactly 2 (v1 then v2)", len(sources), sources)
	}
	if sources[1] != "s3://bucket/redeploy-v2.tar.gz" {
		t.Errorf("second inject delivered %q, want the redeployed v2 tarball", sources[1])
	}
}

// TestAsyncInjectorBoundsConcurrentInjects: launching far more injects than
// maxConcurrentInjects must saturate — but never exceed — the semaphore, and
// every queued attempt must still run to completion once slots free up.
func TestAsyncInjectorBoundsConcurrentInjects(t *testing.T) {
	var mu sync.Mutex
	active, peak := 0, 0
	block := make(chan struct{})
	a := newAsyncInjector(func(ctx context.Context, _ string, _ *vibedv1.VibedApp) (bool, error) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		select {
		case <-block:
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		mu.Unlock()
		return true, nil
	})

	const total = 3 * maxConcurrentInjects
	for i := 0; i < total; i++ {
		app := validApp(fmt.Sprintf("bulk-%d", i))
		a.launch(app.UID, "tok", "10.0.0.1:9000", app)
	}

	waitFor(t, "semaphore saturation", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return active == maxConcurrentInjects
	})
	// Give any over-admitted goroutine a moment to show up before checking
	// the bound.
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if peak > maxConcurrentInjects {
		t.Errorf("peak concurrent injects = %d, exceeds bound %d", peak, maxConcurrentInjects)
	}
	mu.Unlock()

	close(block)
	for i := 0; i < total; i++ {
		app := validApp(fmt.Sprintf("bulk-%d", i))
		uid := app.UID
		waitFor(t, fmt.Sprintf("completion of inject %d", i), func() bool {
			state, res := a.peek(uid, "tok")
			return state == injectDone && res.running
		})
	}
}

// TestAsyncInjectorConsumeAndForget: a done result is consumed exactly once
// (the second peek reports none, so the next Starting pass injects afresh),
// and forget cancels an in-flight attempt outright.
func TestAsyncInjectorConsumeAndForget(t *testing.T) {
	a := newAsyncInjector(func(context.Context, string, *vibedv1.VibedApp) (bool, error) {
		return true, nil
	})
	app := validApp("consume")
	e := a.launch(app.UID, "tok", "10.0.0.1:9000", app)
	<-e.done
	if state, res := a.peek(app.UID, "tok"); state != injectDone || !res.running {
		t.Fatalf("peek = (%v, %+v), want done+running", state, res)
	}
	if state, _ := a.peek(app.UID, "tok"); state != injectNone {
		t.Fatalf("second peek = %v, want none (results are consumed exactly once)", state)
	}

	started := make(chan struct{})
	cancelled := make(chan struct{})
	a2 := newAsyncInjector(func(ctx context.Context, _ string, _ *vibedv1.VibedApp) (bool, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return false, ctx.Err()
	})
	a2.launch(app.UID, "tok", "10.0.0.1:9000", app)
	<-started
	a2.forget(app.UID)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("forget did not cancel the in-flight inject")
	}
	if state, _ := a2.peek(app.UID, "tok"); state != injectNone {
		t.Fatalf("peek after forget = %v, want none", state)
	}
}
