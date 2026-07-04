package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/vibed-project/vibeD/internal/meter"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

type meterRecorder struct{ events []meter.Event }

func (m *meterRecorder) Record(_ context.Context, e meter.Event) { m.events = append(m.events, e) }

// The controller emits an app.ready event when an app first reaches Ready and an
// app.stopped event when it leaves Ready (e.g. on suspend). Transitions that do
// not cross the Ready boundary emit nothing.
func TestLifecycleMetering(t *testing.T) {
	rec := &meterRecorder{}
	app := validApp("metered")
	r := newReconciler(t, app, func(r *Reconciler) { r.Meter = rec })

	// Pending→Claiming and Claiming→Starting cross no Ready boundary.
	runOnce(t, r, app)
	runOnce(t, r, app)
	if len(rec.events) != 0 {
		t.Fatalf("pre-Ready transitions emitted events: %+v", rec.events)
	}

	// Starting→Ready emits exactly one app.ready.
	got, _ := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseReady {
		t.Fatalf("want Ready, got %q", got.Status.Phase)
	}
	if len(rec.events) != 1 || rec.events[0].Kind != "app.ready" {
		t.Fatalf("events after Ready = %+v, want one app.ready", rec.events)
	}
	if e := rec.events[0]; e.App != "metered" || e.Owner != "alice@example.com" || e.Namespace != "default" {
		t.Errorf("app.ready event = %+v", e)
	}

	// Steady-state Ready reconcile emits nothing more.
	runOnce(t, r, app)
	if len(rec.events) != 1 {
		t.Fatalf("steady-state Ready emitted extra events: %+v", rec.events)
	}

	// Suspend → Ready→Suspended emits app.stopped.
	live := &vibedv1.VibedApp{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "metered", Namespace: "default"}, live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Suspended = true
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	got, _ = runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseSuspended {
		t.Fatalf("want Suspended, got %q", got.Status.Phase)
	}
	if len(rec.events) != 2 || rec.events[1].Kind != "app.stopped" {
		t.Fatalf("events after suspend = %+v, want app.stopped", rec.events)
	}
}
