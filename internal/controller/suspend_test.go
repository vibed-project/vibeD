package controller

import (
	"context"
	"testing"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// recordingClaimer behaves like DummyClaimer but records whether ReleaseClaim
// was called, so suspend tests can assert the pod was released.
type recordingClaimer struct{ released *bool }

func (recordingClaimer) EnsureClaim(_ context.Context, app *vibedv1.VibedApp) (bool, string, string, error) {
	return true, "sandbox-" + app.Name, "10.0.0.1", nil
}

func (c recordingClaimer) ReleaseClaim(context.Context, *vibedv1.VibedApp) error {
	*c.released = true
	return nil
}

func TestSuspendReleasesAndParks(t *testing.T) {
	app := validApp("susp")
	app.Spec.Suspended = true
	app.Status = vibedv1.VibedAppStatus{
		Phase:       vibedv1.PhaseReady,
		PodIP:       "10.0.0.1",
		SandboxRef:  "sandbox-susp",
		RouteTarget: "susp-svc.default.svc:8080",
		URL:         "https://susp.test.example.com",
	}
	released := false
	r := newReconciler(t, app, func(r *Reconciler) { r.Claimer = recordingClaimer{released: &released} })

	got, _ := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseSuspended {
		t.Fatalf("phase = %q, want Suspended", got.Status.Phase)
	}
	if !released {
		t.Error("expected ReleaseClaim to be called on suspend")
	}
	if got.Status.PodIP != "" || got.Status.SandboxRef != "" || got.Status.RouteTarget != "" {
		t.Errorf("expected pod/route status cleared, got podIP=%q ref=%q route=%q",
			got.Status.PodIP, got.Status.SandboxRef, got.Status.RouteTarget)
	}
}

func TestSuspendedIsSteadyState(t *testing.T) {
	app := validApp("steady")
	app.Spec.Suspended = true
	app.Status = vibedv1.VibedAppStatus{Phase: vibedv1.PhaseSuspended}
	released := false
	r := newReconciler(t, app, func(r *Reconciler) { r.Claimer = recordingClaimer{released: &released} })

	got, _ := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseSuspended {
		t.Fatalf("phase = %q, want Suspended (steady)", got.Status.Phase)
	}
	if released {
		t.Error("ReleaseClaim must not be called when already Suspended")
	}
}

func TestResumeReClaims(t *testing.T) {
	app := validApp("res")
	app.Spec.Suspended = false
	app.Status = vibedv1.VibedAppStatus{Phase: vibedv1.PhaseSuspended}
	r := newReconciler(t, app) // DummyClaimer

	got, _ := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseClaiming {
		t.Fatalf("phase = %q, want Claiming (restore re-enters the machine)", got.Status.Phase)
	}
}
