package controller

// Asynchronous wrapper around Injector. The in-sandbox agent's /inject
// endpoint is synchronous server-side — it downloads the source tarball,
// extracts it, and starts the user process BEFORE responding (up to 60s) —
// and its HTTP contract is frozen (agent binaries are baked into template
// images, so version skew must not break). The controller therefore goes
// async around the existing call: each inject runs in ONE tracked goroutine
// keyed by (app UID, inject token), and Reconcile only peeks at the tracked
// state instead of parking a worker for the whole HTTP call.
//
// Staleness: a redeploy bumps the spec generation / tarball ref and a
// sandbox-pod replacement changes the target, so the token captures all
// three. An in-flight attempt for an old token is cancelled and its result
// discarded — it must never drive state transitions for the new desired
// state.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// maxConcurrentInjects bounds how many Injector.Inject calls run at once
// across all apps, so a mass redeploy queues injects instead of opening an
// unbounded number of concurrent agent connections. Injection is I/O-bound
// (tarball download inside the sandbox), so a moderate bound keeps aggregate
// deploy throughput high without stampeding the object store or the network.
const maxConcurrentInjects = 8

// injectAttemptTimeout is the per-attempt budget, mirroring HTTPInjector's
// default 60s Timeout. Applied here too so a misbehaving Injector
// implementation cannot pin a semaphore slot (and its goroutine) forever.
const injectAttemptTimeout = 60 * time.Second

// injectResultTTL evicts completed results that were never consumed (e.g. the
// app was deleted while an inject was in flight and no reconcile came back to
// peek), so the tracker map cannot leak. Eviction is lazy, on the next
// launch/peek of any app.
const injectResultTTL = 5 * time.Minute

// defaultInjectGrace is how long the launching reconcile pass waits for a
// freshly started inject to finish inline before going async (condition +
// requeue). Fast injects (small tarballs, warm caches, the DummyInjector)
// complete within the window and keep the synchronous path's single-pass
// Starting→Ready hop; slow ones stop parking the worker after the grace
// elapses. Kept small relative to the 60s inject budget.
const defaultInjectGrace = 250 * time.Millisecond

// injectToken identifies WHAT an inject delivers WHERE. If any component
// changes — a redeploy bumps metadata.generation and the tarball ref, a
// sandbox-pod replacement changes the target — an in-flight inject for the
// old token is stale and its result is discarded.
func injectToken(app *vibedv1.VibedApp, target string) string {
	return fmt.Sprintf("%s|%d|%s", target, app.Generation, app.Spec.Source.TarballRef)
}

// injectState is the tracked lifecycle of one (uid, token) inject attempt.
type injectState int

const (
	injectNone     injectState = iota // nothing tracked for this (uid, token)
	injectInflight                    // goroutine launched, no result yet
	injectDone                        // result recorded, not yet consumed
)

// injectResult is what Injector.Inject returned for a completed attempt.
type injectResult struct {
	running     bool
	err         error
	completedAt time.Time
}

// injectEntry is one tracked attempt. done is closed when the result is
// recorded so the launching reconcile can wait a short grace window on it.
type injectEntry struct {
	token  string
	state  injectState
	result injectResult
	done   chan struct{}
	cancel context.CancelFunc
}

// asyncInjector tracks at most one in-flight inject per app UID. All methods
// are safe for concurrent use by reconcile workers.
type asyncInjector struct {
	// inject performs the actual (synchronous) injection; the Reconciler wires
	// a closure over its Injector so test overrides keep working.
	inject func(ctx context.Context, target string, app *vibedv1.VibedApp) (bool, error)

	mu sync.Mutex
	// base is the lifecycle context attempts derive from — the manager's
	// context once run is started, NOT any reconcile context (those are
	// cancelled the moment Reconcile returns, which would kill the attempt
	// the async design exists to keep alive).
	base    context.Context
	entries map[types.UID]*injectEntry
	sem     chan struct{}
}

func newAsyncInjector(inject func(ctx context.Context, target string, app *vibedv1.VibedApp) (bool, error)) *asyncInjector {
	return &asyncInjector{
		inject:  inject,
		base:    context.Background(),
		entries: make(map[types.UID]*injectEntry),
		sem:     make(chan struct{}, maxConcurrentInjects),
	}
}

// run binds the tracker's goroutine lifecycle to the manager: SetupWithManager
// registers it via mgr.Add, so in-flight injects derive from the manager
// context and abort promptly on shutdown instead of leaking.
func (a *asyncInjector) run(ctx context.Context) error {
	a.mu.Lock()
	a.base = ctx
	a.mu.Unlock()
	<-ctx.Done()
	return nil
}

// step drives one reconcile pass worth of injection for (uid, token):
//
//	nothing tracked → launch a goroutine and give it a grace window to finish
//	inline (fast injects keep the sync path's single-pass behavior);
//	in flight     → report it, non-blocking;
//	done          → consume and return the result (exactly once — the next
//	                Starting pass injects afresh, matching the sync path's
//	                retry-on-requeue semantics).
//
// A tracked attempt whose token no longer matches is stale: it is cancelled,
// its result discarded, and a fresh attempt launched.
func (a *asyncInjector) step(uid types.UID, token, target string, app *vibedv1.VibedApp, grace time.Duration) (injectState, injectResult) {
	if state, res := a.peek(uid, token); state != injectNone {
		return state, res
	}
	e := a.launch(uid, token, target, app)
	select {
	case <-e.done:
		if state, res := a.peek(uid, token); state != injectNone {
			return state, res
		}
		// Overwritten between completion and peek (racing redeploy): the
		// next pass observes the fresh attempt.
		return injectInflight, injectResult{}
	case <-time.After(grace):
		return injectInflight, injectResult{}
	}
}

// peek reports the tracked state for (uid, token) without blocking. A done
// result is consumed: the entry is removed so a later Starting pass starts a
// fresh inject (re-injection is idempotent agent-side). A tracked entry with
// a different token reads as none — the caller's launch will overwrite it.
func (a *asyncInjector) peek(uid types.UID, token string) (injectState, injectResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictExpiredLocked()
	e, ok := a.entries[uid]
	if !ok || e.token != token {
		return injectNone, injectResult{}
	}
	if e.state != injectDone {
		return injectInflight, injectResult{}
	}
	delete(a.entries, uid)
	return injectDone, e.result
}

// launch starts (or returns the already-tracked) inject attempt for
// (uid, token). A tracked attempt under a different token is stale — a
// redeploy or pod replacement changed what must be injected — so it is
// cancelled (its eventual result is discarded via the map overwrite) and a
// fresh attempt takes its place.
func (a *asyncInjector) launch(uid types.UID, token, target string, app *vibedv1.VibedApp) *injectEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictExpiredLocked()
	if e, ok := a.entries[uid]; ok {
		if e.token == token {
			return e
		}
		e.cancel()
	}
	ctx, cancel := context.WithCancel(a.base)
	e := &injectEntry{token: token, state: injectInflight, done: make(chan struct{}), cancel: cancel}
	a.entries[uid] = e
	// Deep-copy: the goroutine outlives Reconcile, which keeps mutating app.
	go a.doInject(ctx, e, target, app.DeepCopy())
	return e
}

// forget drops any tracked attempt for uid, cancelling it if still in flight.
// Called when the app is deleted, suspended, or regresses out of Starting.
func (a *asyncInjector) forget(uid types.UID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e, ok := a.entries[uid]; ok {
		e.cancel()
		delete(a.entries, uid)
	}
}

// doInject is the single goroutine per (uid, token): acquire a semaphore
// slot, run the existing synchronous Inject under the existing 60s budget,
// record the result.
func (a *asyncInjector) doInject(ctx context.Context, e *injectEntry, target string, app *vibedv1.VibedApp) {
	select {
	case a.sem <- struct{}{}:
	case <-ctx.Done():
		// Cancelled (stale token, forget, or shutdown) while queued.
		a.record(e, false, ctx.Err())
		return
	}
	defer func() { <-a.sem }()

	ictx, cancel := context.WithTimeout(ctx, injectAttemptTimeout)
	defer cancel()
	running, err := a.inject(ictx, target, app)
	a.record(e, running, err)
}

// record publishes the attempt's result on its entry. Staleness is handled by
// the map, not here: an overwritten/forgotten entry is simply unreachable, so
// its result can never be consumed.
func (a *asyncInjector) record(e *injectEntry, running bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e.result = injectResult{running: running, err: err, completedAt: time.Now()}
	e.state = injectDone
	e.cancel() // attempt finished; release the lifecycle-ctx link
	close(e.done)
}

// evictExpiredLocked lazily drops completed-but-unconsumed results older than
// injectResultTTL. Callers hold a.mu.
func (a *asyncInjector) evictExpiredLocked() {
	now := time.Now()
	for uid, e := range a.entries {
		if e.state == injectDone && now.Sub(e.result.completedAt) > injectResultTTL {
			delete(a.entries, uid)
		}
	}
}
