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
	if ev.Phase != "Ready" || ev.Status != "Ready" || ev.URL != "https://demo.example.com" {
		t.Fatalf("unexpected transition event: %+v", ev)
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
