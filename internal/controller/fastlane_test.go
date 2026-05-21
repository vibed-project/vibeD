package controller

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// fakeFastLane records deploys and returns a fixed target (or an error).
type fakeFastLane struct {
	target  string
	err     error
	deploys int
	removes int
}

func (f *fakeFastLane) Deploy(_ context.Context, _ *vibedv1.VibedApp) (string, error) {
	f.deploys++
	return f.target, f.err
}

func (f *fakeFastLane) Remove(_ context.Context, _ *vibedv1.VibedApp) error {
	f.removes++
	return nil
}

func workerdApp(name string) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("uid-" + name)},
		Spec: vibedv1.VibedAppSpec{
			Owner:   "alice@example.com",
			Source:  vibedv1.Source{TarballRef: "s3://bucket/" + name + ".tar.gz"},
			Runtime: vibedv1.Runtime{Lane: vibedv1.LaneFast, Template: "workerd"},
		},
	}
}

func TestFastLaneDeployReachesReady(t *testing.T) {
	app := workerdApp("worker-app")
	fl := &fakeFastLane{target: "vibed-workerd-1.vibed-workerd.vibed-system.svc:9137"}
	r := newReconciler(t, app, func(r *Reconciler) { r.FastLane = fl })

	got, res := runOnce(t, r, app)

	if got.Status.Phase != vibedv1.PhaseReady {
		t.Fatalf("phase = %q, want Ready", got.Status.Phase)
	}
	if fl.deploys != 1 {
		t.Errorf("deploys = %d, want 1", fl.deploys)
	}
	// PodIP carries the full host:port for fast-lane routing.
	if got.Status.PodIP != fl.target {
		t.Errorf("PodIP = %q, want %q", got.Status.PodIP, fl.target)
	}
	if got.Status.URL == "" {
		t.Error("expected a URL once Ready")
	}
	if got.Status.LastDeployedAt == nil {
		t.Error("expected LastDeployedAt set")
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Ready fast-lane app should not requeue, got %v", res.RequeueAfter)
	}
}

func TestFastLaneDeployErrorRequeues(t *testing.T) {
	app := workerdApp("flaky")
	fl := &fakeFastLane{err: errors.New("loader unreachable")}
	r := newReconciler(t, app, func(r *Reconciler) { r.FastLane = fl })

	got, res := runOnce(t, r, app)

	if got.Status.Phase == vibedv1.PhaseReady {
		t.Error("should not be Ready when the loader deploy fails")
	}
	if res.RequeueAfter == 0 {
		t.Error("expected requeue after a fast-lane deploy failure")
	}
	if !hasConditionWithReason(got, ConditionReady, metav1.ConditionFalse, ReasonClaimFailed) {
		t.Errorf("expected Ready=False on deploy failure, conditions=%+v", got.Status.Conditions)
	}
}

func TestFastLaneSteadyStateIsNoOp(t *testing.T) {
	app := workerdApp("steady")
	app.Status.Phase = vibedv1.PhaseReady
	app.Status.URL = "https://x.vibed.example.com"
	app.Status.PodIP = "vibed-workerd-0.svc:9100"
	fl := &fakeFastLane{target: "should-not-be-called"}
	r := newReconciler(t, app, func(r *Reconciler) { r.FastLane = fl })

	_, _ = runOnce(t, r, app)
	if fl.deploys != 0 {
		t.Errorf("Ready fast-lane app should not redeploy, deploys=%d", fl.deploys)
	}
}

// A workerd app on a reconciler without a fast lane configured fails clearly
// via the DummyFastLaneDeployer rather than nil-panicking.
func TestFastLaneUnconfiguredFailsCleanly(t *testing.T) {
	app := workerdApp("no-fastlane")
	r := newReconciler(t, app) // no FastLane override → DummyFastLaneDeployer

	got, res := runOnce(t, r, app)
	if got.Status.Phase == vibedv1.PhaseReady {
		t.Error("should not be Ready without a configured fast lane")
	}
	if res.RequeueAfter == 0 {
		t.Error("expected requeue")
	}
}
