package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/events"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

func bridgeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func nextBusEvent(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bus event")
		return events.Event{}
	}
}

// TestEventBridgePublishesTransitions drives the bridge with a fake watchable
// client and asserts: first sight and phase transitions publish exactly once,
// a write with unchanged phase/URL publishes nothing, and a delete publishes
// artifact.deleted.
func TestEventBridgePublishesTransitions(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(bridgeScheme(t)).Build()
	bus := events.NewEventBus()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	b := &eventBridge{
		client:    c,
		namespace: "vibed-apps",
		bus:       bus,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		last:      make(map[types.UID]bridgeState),
		watchStarted: func() {
			select {
			case <-started:
			default:
				close(started)
			}
		},
	}

	sub, unsub := bus.Subscribe(ctx)
	defer unsub()

	go b.Run(ctx)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("watch never established")
	}

	// The fake tracker's watch does not replay pre-existing objects, so
	// create the app only after the watch is receiving.
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "vibed-apps", UID: types.UID("uid-demo")},
		Spec: vibedv1.VibedAppSpec{
			Owner:   "alice",
			Runtime: vibedv1.Runtime{Lane: vibedv1.LaneGeneral, Template: "node-24"},
		},
		Status: vibedv1.VibedAppStatus{Phase: vibedv1.PhasePending},
	}
	if err := c.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// First sight publishes a status_changed event.
	ev := nextBusEvent(t, sub)
	if ev.Type != events.ArtifactStatusChanged {
		t.Fatalf("expected %s, got %s", events.ArtifactStatusChanged, ev.Type)
	}
	if ev.ArtifactID != "demo" || ev.OwnerID != "alice" || ev.Phase != "Pending" {
		t.Fatalf("unexpected first-sight event: %+v", ev)
	}

	// A genuine phase transition publishes exactly once, carrying the
	// enriched fields the dashboard needs.
	app.Status.Phase = vibedv1.PhaseReady
	app.Status.URL = "https://demo.example.com"
	if err := c.Update(ctx, app); err != nil {
		t.Fatalf("update to Ready: %v", err)
	}
	ev = nextBusEvent(t, sub)
	// Status carries the /v1 display enum (Ready → "running"), not the raw
	// phase; Phase carries the raw phase.
	wantStatus := string(vibedv1.StatusFromPhase(vibedv1.PhaseReady))
	if ev.Phase != "Ready" || ev.Status != wantStatus || ev.URL != "https://demo.example.com" {
		t.Fatalf("unexpected transition event (want status %q): %+v", wantStatus, ev)
	}
	if ev.Name != "demo" || ev.Template != "node-24" {
		t.Fatalf("expected name/template on transition event, got %+v", ev)
	}

	// A write with the SAME phase/URL must publish nothing. Proven without
	// sleeping: the next event on the bus after it must be the delete below
	// (per-subscriber order is preserved).
	app.Annotations = map[string]string{"touched": "true"}
	if err := c.Update(ctx, app); err != nil {
		t.Fatalf("no-op status update: %v", err)
	}
	if err := c.Delete(ctx, app); err != nil {
		t.Fatalf("delete app: %v", err)
	}

	ev = nextBusEvent(t, sub)
	if ev.Type != events.ArtifactDeleted {
		t.Fatalf("same-phase write should publish nothing; next event = %+v", ev)
	}
	if ev.ArtifactID != "demo" || ev.OwnerID != "alice" {
		t.Fatalf("unexpected delete event: %+v", ev)
	}
}

// TestEventBridgeStopsOnCancel asserts Run exits promptly when the server
// context is cancelled rather than looping on reconnect.
func TestEventBridgeStopsOnCancel(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(bridgeScheme(t)).Build()

	ctx, cancel := context.WithCancel(context.Background())
	b := &eventBridge{
		client:    c,
		namespace: "vibed-apps",
		bus:       events.NewEventBus(),
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		last:      make(map[types.UID]bridgeState),
	}

	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on context cancel")
	}
}

// newTestBridge builds a bridge with no client, for driving the reconcile
// (sync) directly with hand-built list results.
func newTestBridge(bus *events.EventBus) *eventBridge {
	return &eventBridge{
		bus:    bus,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		last:   make(map[types.UID]bridgeState),
	}
}

// appFixture builds a VibedApp the way a List would return it.
func appFixture(name, uid, owner string, phase vibedv1.Phase, url string) vibedv1.VibedApp {
	return vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "vibed-apps", UID: types.UID(uid)},
		Spec: vibedv1.VibedAppSpec{
			Owner:   owner,
			Runtime: vibedv1.Runtime{Lane: vibedv1.LaneGeneral, Template: "node-24"},
		},
		Status: vibedv1.VibedAppStatus{Phase: phase, URL: url},
	}
}

// expectNoBusEvent asserts nothing more lands on the bus within the window —
// used to prove sync doesn't republish an unchanged app.
func expectNoBusEvent(t *testing.T, ch <-chan events.Event, within time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("expected no further event, got %+v", ev)
	case <-time.After(within):
	}
}

// TestEventBridgeSyncReconcilesState drives the reconnect reconcile directly:
// an app that vanished from the namespace while the bridge was disconnected
// (so its Deleted event was never observed) is reaped with a synthetic
// artifact.deleted and dropped from last, while an app that survived unchanged
// is re-evaluated without republishing.
func TestEventBridgeSyncReconcilesState(t *testing.T) {
	bus := events.NewEventBus()
	b := newTestBridge(bus)

	// A survives the next list unchanged; B was deleted during the disconnect.
	b.last[types.UID("uid-a")] = bridgeState{name: "app-a", owner: "alice", phase: "Ready", url: "https://a.example.com"}
	b.last[types.UID("uid-b")] = bridgeState{name: "app-b", owner: "bob", phase: "Ready", url: "https://b.example.com"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, unsub := bus.Subscribe(ctx)
	defer unsub()

	b.sync([]vibedv1.VibedApp{appFixture("app-a", "uid-a", "alice", vibedv1.PhaseReady, "https://a.example.com")})

	// B is reaped with a synthetic delete carrying its retained name/owner.
	ev := nextBusEvent(t, sub)
	if ev.Type != events.ArtifactDeleted || ev.ArtifactID != "app-b" || ev.OwnerID != "bob" {
		t.Fatalf("expected synthetic delete for app-b, got %+v", ev)
	}
	// A is unchanged, so nothing else is published.
	expectNoBusEvent(t, sub, 200*time.Millisecond)

	if _, ok := b.last[types.UID("uid-b")]; ok {
		t.Fatal("deleted app-b should have been pruned from last")
	}
	if _, ok := b.last[types.UID("uid-a")]; !ok {
		t.Fatal("surviving app-a should remain in last")
	}
}

// TestEventBridgeSyncRepublishesGapTransition asserts that a status change an
// app made while the bridge was disconnected is republished exactly once by
// the reconcile.
func TestEventBridgeSyncRepublishesGapTransition(t *testing.T) {
	bus := events.NewEventBus()
	b := newTestBridge(bus)
	b.last[types.UID("uid-a")] = bridgeState{name: "app-a", owner: "alice", phase: "Starting"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, unsub := bus.Subscribe(ctx)
	defer unsub()

	b.sync([]vibedv1.VibedApp{appFixture("app-a", "uid-a", "alice", vibedv1.PhaseReady, "https://a.example.com")})

	ev := nextBusEvent(t, sub)
	if ev.Type != events.ArtifactStatusChanged || ev.Phase != "Ready" || ev.URL != "https://a.example.com" {
		t.Fatalf("expected republished Ready transition, got %+v", ev)
	}
	expectNoBusEvent(t, sub, 200*time.Millisecond)
}

// TestEventBridgePublishesDisplayStatus asserts the enriched status_changed
// event carries the /v1 display enum in Status and the raw CR phase in Phase.
func TestEventBridgePublishesDisplayStatus(t *testing.T) {
	bus := events.NewEventBus()
	b := newTestBridge(bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, unsub := bus.Subscribe(ctx)
	defer unsub()

	b.sync([]vibedv1.VibedApp{appFixture("demo", "uid-demo", "alice", vibedv1.PhaseReady, "https://demo.example.com")})

	ev := nextBusEvent(t, sub)
	want := string(vibedv1.StatusFromPhase(vibedv1.PhaseReady))
	if want != "running" {
		t.Fatalf("guard: /v1 display mapping for Ready changed to %q", want)
	}
	if ev.Status != want {
		t.Fatalf("Status should be the display enum %q, got %q", want, ev.Status)
	}
	if ev.Phase != "Ready" {
		t.Fatalf("Phase should be the raw CR phase \"Ready\", got %q", ev.Phase)
	}
}
