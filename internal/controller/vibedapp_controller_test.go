package controller

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// runOnce reconciles once and returns the fresh object + result. The fake
// client is configured with a status subresource so Status().Update behaves
// like the real apiserver.
func runOnce(t *testing.T, r *Reconciler, app *vibedv1.VibedApp) (*vibedv1.VibedApp, reconcile.Result) {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &vibedv1.VibedApp{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	return got, res
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func newReconciler(t *testing.T, app *vibedv1.VibedApp, overrides ...func(*Reconciler)) *Reconciler {
	t.Helper()
	s := newScheme(t)
	// Snapshot the desired initial status before the fake client strips it
	// off (Create on a status-subresource type drops .status silently).
	wantStatus := app.Status.DeepCopy()
	app.Status = vibedv1.VibedAppStatus{}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(app).
		WithStatusSubresource(&vibedv1.VibedApp{}).
		Build()

	// Re-apply the requested status via Status().Update if the test seeded
	// any (e.g. starting mid-state-machine).
	if wantStatus != nil && wantStatus.Phase != "" {
		live := &vibedv1.VibedApp{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, live); err != nil {
			t.Fatalf("seed get: %v", err)
		}
		live.Status = *wantStatus
		if err := c.Status().Update(context.Background(), live); err != nil {
			t.Fatalf("seed status: %v", err)
		}
	}

	r := &Reconciler{
		Client:  c,
		Scheme:  s,
		Claimer: DummyClaimer{},
		Probe:   DummyAgentProbe{},
		Router:  DummyRouter{Domain: "test.example.com"},
	}
	for _, o := range overrides {
		o(r)
	}
	return r
}

func validApp(name string) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid-12345678")},
		Spec: vibedv1.VibedAppSpec{
			Owner:   "alice@example.com",
			Source:  vibedv1.Source{TarballRef: "s3://bucket/" + name + ".tar.gz"},
			Runtime: vibedv1.Runtime{Lane: vibedv1.LaneGeneral, Template: "node-24"},
		},
	}
}

// TestHappyPath walks a fresh VibedApp through Pending → Claiming → Starting
// → Ready in successive reconciles and asserts the recorded artifacts (URL,
// SandboxRef, LastDeployedAt, Ready condition) along the way.
func TestHappyPath(t *testing.T) {
	app := validApp("hello")
	r := newReconciler(t, app)

	// Reconcile 1: "" → Claiming.
	got, _ := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseClaiming {
		t.Fatalf("after first reconcile, phase=%q want Claiming", got.Status.Phase)
	}

	// Reconcile 2: Claiming → Starting; SandboxRef populated.
	got, _ = runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Fatalf("after second reconcile, phase=%q want Starting", got.Status.Phase)
	}
	if got.Status.SandboxRef == "" {
		t.Error("expected SandboxRef to be populated after Claiming")
	}

	// Reconcile 3: Starting → Ready; URL + LastDeployedAt populated.
	got, _ = runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseReady {
		t.Fatalf("after third reconcile, phase=%q want Ready", got.Status.Phase)
	}
	if got.Status.URL == "" {
		t.Error("expected URL to be set when Ready")
	}
	if got.Status.LastDeployedAt == nil {
		t.Error("expected LastDeployedAt to be set when Ready")
	}
	if !hasCondition(got, ConditionReady, metav1.ConditionTrue) {
		t.Errorf("expected Ready=True condition, got %+v", got.Status.Conditions)
	}

	// Reconcile 4: Ready → Ready (no-op).
	before := got.Status.DeepCopy()
	got, res := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseReady {
		t.Errorf("steady-state Ready reconcile flipped phase to %q", got.Status.Phase)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("steady-state should not requeue, got RequeueAfter=%v", res.RequeueAfter)
	}
	if !statusEqual(before, &got.Status) {
		t.Errorf("steady-state reconcile mutated status")
	}
}

func TestMissingSourceFails(t *testing.T) {
	app := validApp("no-source")
	app.Spec.Source = vibedv1.Source{} // neither tarball nor git
	r := newReconciler(t, app)

	got, _ := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseFailed {
		t.Fatalf("phase=%q want Failed", got.Status.Phase)
	}
	if !hasConditionWithReason(got, ConditionSourceValid, metav1.ConditionFalse, ReasonSourceMissing) {
		t.Errorf("expected SourceValid=False/SourceMissing, got %+v", got.Status.Conditions)
	}
}

func TestAmbiguousSourceFails(t *testing.T) {
	app := validApp("dual-source")
	app.Spec.Source.GitRef = "https://github.com/example/repo@abc"
	r := newReconciler(t, app)

	got, _ := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseFailed {
		t.Fatalf("phase=%q want Failed", got.Status.Phase)
	}
	if !hasConditionWithReason(got, ConditionSourceValid, metav1.ConditionFalse, ReasonSourceAmbiguous) {
		t.Errorf("expected SourceValid=False/SourceAmbiguous, got %+v", got.Status.Conditions)
	}
}

func TestClaimErrorRequeues(t *testing.T) {
	app := validApp("flaky-claim")
	// Skip past Pending → Claiming so the failing branch executes.
	app.Status.Phase = vibedv1.PhaseClaiming
	r := newReconciler(t, app, func(r *Reconciler) {
		r.Claimer = failingClaimer{err: errors.New("pool empty")}
	})

	got, res := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseClaiming {
		t.Errorf("phase should stay Claiming on transient error, got %q", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected RequeueAfter to be set on claim failure")
	}
	if !hasConditionWithReason(got, ConditionReady, metav1.ConditionFalse, ReasonClaimFailed) {
		t.Errorf("expected Ready=False/ClaimFailed, got %+v", got.Status.Conditions)
	}
}

func TestAgentNotReadyRequeues(t *testing.T) {
	app := validApp("agent-pending")
	app.Status.Phase = vibedv1.PhaseStarting
	app.Status.SandboxRef = "sb-xyz"
	r := newReconciler(t, app, func(r *Reconciler) {
		r.Probe = staticProbe{ready: false}
	})

	got, res := runOnce(t, r, app)
	if got.Status.Phase != vibedv1.PhaseStarting {
		t.Errorf("phase should stay Starting while agent reports not-ready, got %q", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected RequeueAfter while waiting on agent")
	}
}

// --- helpers ---

func hasCondition(app *vibedv1.VibedApp, condType string, status metav1.ConditionStatus) bool {
	for _, c := range app.Status.Conditions {
		if c.Type == condType && c.Status == status {
			return true
		}
	}
	return false
}

func hasConditionWithReason(app *vibedv1.VibedApp, condType string, status metav1.ConditionStatus, reason string) bool {
	for _, c := range app.Status.Conditions {
		if c.Type == condType && c.Status == status && c.Reason == reason {
			return true
		}
	}
	return false
}

type failingClaimer struct{ err error }

func (f failingClaimer) Claim(_ context.Context, _ *vibedv1.VibedApp) (string, error) {
	return "", f.err
}

type staticProbe struct {
	ready bool
	err   error
}

func (s staticProbe) IsReady(_ context.Context, _ string) (bool, error) { return s.ready, s.err }

// quiet linter — silence unused if helpers drift.
var _ = client.IgnoreNotFound
