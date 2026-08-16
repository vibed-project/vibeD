package router

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/vibed-project/vibeD/internal/controller"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func readyApp(name string) *vibedv1.VibedApp {
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: vibedv1.VibedAppSpec{
			Owner:   "alice@example.com",
			Source:  vibedv1.Source{TarballRef: "s3://x"},
			Runtime: vibedv1.Runtime{Lane: vibedv1.LaneGeneral, Template: "node-24"},
		},
	}
	return app
}

func newReconciler(t *testing.T, fc *fakeCaddy, app *vibedv1.VibedApp) (*Reconciler, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(fc.handler())
	t.Cleanup(srv.Close)

	s := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(app).
		WithStatusSubresource(&vibedv1.VibedApp{}).
		Build()

	// Seed status separately so the fake client preserves it.
	if app.Status.Phase != "" {
		live := &vibedv1.VibedApp{}
		_ = c.Get(context.Background(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, live)
		live.Status = app.Status
		_ = c.Status().Update(context.Background(), live)
	}

	r := &Reconciler{
		Client:  c,
		Scheme:  s,
		Caddy:   NewCaddyClient(srv.URL),
		AppPort: 8080,
	}
	return r, srv
}

func reconcileOnce(t *testing.T, r *Reconciler, app *vibedv1.VibedApp) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// TestReadyAppGetsRoute is the happy path: Phase=Ready + URL + PodIP →
// EnsureRoute called, route shows up in fake Caddy.
func TestReadyAppGetsRoute(t *testing.T) {
	app := readyApp("happy")
	app.Status = vibedv1.VibedAppStatus{
		Phase: vibedv1.PhaseReady,
		URL:   "https://" + controller.AppLabel(app) + ".vibed.example.com",
		PodIP: "10.0.0.42",
	}
	fc := newFakeCaddy()
	r, _ := newReconciler(t, fc, app)

	reconcileOnce(t, r, app)

	id := RoutePrefix + controller.AppLabel(app)
	body, ok := fc.routes[id]
	if !ok {
		t.Fatalf("route %s not stored; routes=%v", id, fc.routes)
	}

	var route map[string]any
	if err := json.Unmarshal(body, &route); err != nil {
		t.Fatalf("unmarshal route: %v", err)
	}

	bodyStr := string(body)
	if !strings.Contains(bodyStr, "10.0.0.42:8080") {
		t.Errorf("route body missing upstream 10.0.0.42:8080: %s", bodyStr)
	}
	wantHost := controller.AppLabel(app) + ".vibed.example.com"
	if !strings.Contains(bodyStr, wantHost) {
		t.Errorf("route body missing host %s: %s", wantHost, bodyStr)
	}
}

// TestRouteTargetPreferredOverPodIP: when the controller has published a
// stable RouteTarget (the per-app Service DNS name), the router proxies to it
// verbatim and ignores the raw PodIP — that's the whole point of the Service
// indirection (PodIP goes stale on pod restart).
func TestRouteTargetPreferredOverPodIP(t *testing.T) {
	app := readyApp("rt-app")
	target := "vibed-app-" + controller.AppLabel(app) + ".vibed-apps.svc.cluster.local:8080"
	app.Status = vibedv1.VibedAppStatus{
		Phase:       vibedv1.PhaseReady,
		URL:         "https://" + controller.AppLabel(app) + ".vibed.example.com",
		PodIP:       "10.0.0.42",
		RouteTarget: target,
	}
	fc := newFakeCaddy()
	r, _ := newReconciler(t, fc, app)

	reconcileOnce(t, r, app)

	id := RoutePrefix + controller.AppLabel(app)
	body, ok := fc.routes[id]
	if !ok {
		t.Fatalf("route %s not stored; routes=%v", id, fc.routes)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, target) {
		t.Errorf("route should use RouteTarget %q: %s", target, bodyStr)
	}
	if strings.Contains(bodyStr, "10.0.0.42") {
		t.Errorf("route must NOT fall back to PodIP when RouteTarget is set: %s", bodyStr)
	}
}

// TestRouteHostStripsPort: a dev URL carries a port (http://x.localhost:18080),
// but the Caddy host matcher must be the bare hostname — Caddy strips the port
// from the request Host before matching, so a port in the matcher never hits.
func TestRouteHostStripsPort(t *testing.T) {
	app := readyApp("ported")
	label := controller.AppLabel(app)
	app.Status = vibedv1.VibedAppStatus{
		Phase:       vibedv1.PhaseReady,
		URL:         "http://" + label + ".localhost:18080",
		RouteTarget: "vibed-app-" + label + ".vibed-apps.svc.cluster.local:8080",
	}
	fc := newFakeCaddy()
	r, _ := newReconciler(t, fc, app)

	reconcileOnce(t, r, app)

	id := RoutePrefix + label
	body, ok := fc.routes[id]
	if !ok {
		t.Fatalf("route %s not stored; routes=%v", id, fc.routes)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `"`+label+`.localhost"`) {
		t.Errorf("host matcher should be the bare hostname: %s", bodyStr)
	}
	if strings.Contains(bodyStr, label+".localhost:18080") {
		t.Errorf("host matcher must NOT include the port: %s", bodyStr)
	}
}

// TestNonReadyAppDropsRoute covers the "stale state" case: an app is in
// Claiming/Starting/Failed → router actively deletes any existing route
// for it (which Caddy returns 404 for if there was none — fine).
func TestNonReadyAppDropsRoute(t *testing.T) {
	app := readyApp("not-ready")
	app.Status.Phase = vibedv1.PhaseClaiming
	app.Status.URL = ""
	app.Status.PodIP = ""

	fc := newFakeCaddy()
	id := RoutePrefix + controller.AppLabel(app)
	// Pretend a stale route is sitting there from a previous Ready state.
	fc.routes[id] = json.RawMessage(`{"@id":"` + id + `"}`)

	r, _ := newReconciler(t, fc, app)
	reconcileOnce(t, r, app)

	if _, present := fc.routes[id]; present {
		t.Errorf("stale route should be deleted when app is non-Ready")
	}
}

// TestDeletedAppDropsRoute covers the IsNotFound path: VibedApp deleted →
// reconciler computes the id from the request key and deletes from Caddy.
func TestDeletedAppDropsRoute(t *testing.T) {
	// Build a reconciler with no VibedApps in the cache. The route is
	// pre-existing in Caddy and should be removed.
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()

	fc := newFakeCaddy()
	stub := &vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gone"}}
	id := RoutePrefix + controller.AppLabel(stub)
	fc.routes[id] = json.RawMessage(`{"@id":"` + id + `"}`)
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	r := &Reconciler{
		Client:  c,
		Scheme:  s,
		Caddy:   NewCaddyClient(srv.URL),
		AppPort: 8080,
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, present := fc.routes[id]; present {
		t.Errorf("route should be removed when VibedApp is gone")
	}
}

// TestRoutingFieldsChanged pins down which VibedApp updates wake the router.
// The expensive failure mode is the controller's status churn (condition
// LastTransitionTime bumps) turning into Caddy admin PATCHes; the dangerous
// one is a real routing change being filtered out. Both directions are
// covered here.
func TestRoutingFieldsChanged(t *testing.T) {
	base := func() *vibedv1.VibedApp {
		app := readyApp("pred")
		app.Status = vibedv1.VibedAppStatus{
			Phase:       vibedv1.PhaseReady,
			URL:         "https://" + controller.AppLabel(app) + ".vibed.example.com",
			PodIP:       "10.0.0.42",
			RouteTarget: "vibed-app-x.vibed-apps.svc.cluster.local:8080",
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				Reason:             "Up",
				LastTransitionTime: metav1.NewTime(time.Unix(1000, 0)),
			}},
		}
		return app
	}

	cases := []struct {
		name   string
		mutate func(app *vibedv1.VibedApp)
		want   bool
	}{
		{
			name:   "no change",
			mutate: func(*vibedv1.VibedApp) {},
			want:   false,
		},
		{
			// The churn this predicate exists to swallow: the controller
			// rewriting conditions without touching anything routing-relevant.
			name: "condition-only change",
			mutate: func(app *vibedv1.VibedApp) {
				app.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Unix(2000, 0))
				app.Status.Conditions[0].Reason = "StillUp"
			},
			want: false,
		},
		{
			// Non-routing status fields the controller updates alongside
			// conditions must not wake the router either.
			name: "sandboxRef/snapshotRef/lastDeployedAt change",
			mutate: func(app *vibedv1.VibedApp) {
				app.Status.SandboxRef = "sandbox-abc"
				app.Status.SnapshotRef = "s3://snap"
				now := metav1.Now()
				app.Status.LastDeployedAt = &now
			},
			want: false,
		},
		{
			name:   "phase change",
			mutate: func(app *vibedv1.VibedApp) { app.Status.Phase = vibedv1.PhaseFailed },
			want:   true,
		},
		{
			name:   "url change",
			mutate: func(app *vibedv1.VibedApp) { app.Status.URL = "https://other.vibed.example.com" },
			want:   true,
		},
		{
			name:   "route target change",
			mutate: func(app *vibedv1.VibedApp) { app.Status.RouteTarget = "vibed-app-y.vibed-apps.svc.cluster.local:8080" },
			want:   true,
		},
		{
			name:   "pod ip change",
			mutate: func(app *vibedv1.VibedApp) { app.Status.PodIP = "10.0.0.99" },
			want:   true,
		},
		{
			name: "deletion timestamp set",
			mutate: func(app *vibedv1.VibedApp) {
				now := metav1.Now()
				app.DeletionTimestamp = &now
			},
			want: true,
		},
		{
			name:   "suspended flip",
			mutate: func(app *vibedv1.VibedApp) { app.Spec.Suspended = true },
			want:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldApp, newApp := base(), base()
			tc.mutate(newApp)
			if got := routingFieldsChanged(oldApp, newApp); got != tc.want {
				t.Errorf("routingFieldsChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRoutingPredicateEvents exercises the predicate.Funcs wrapper:
// Create/Delete/Generic always pass (Delete is how the router learns to drop
// a route), Update defers to routingFieldsChanged, and a non-VibedApp object
// fails open rather than dropping the event.
func TestRoutingPredicateEvents(t *testing.T) {
	p := routingPredicate()
	app := readyApp("events")
	app.Status.Phase = vibedv1.PhaseReady

	if !p.Create(event.CreateEvent{Object: app}) {
		t.Error("Create events must always pass")
	}
	if !p.Delete(event.DeleteEvent{Object: app}) {
		t.Error("Delete events must always pass")
	}
	if !p.Generic(event.GenericEvent{Object: app}) {
		t.Error("Generic events must always pass")
	}

	// Status-churn update → filtered.
	churned := app.DeepCopy()
	churned.Status.Conditions = []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Up",
		LastTransitionTime: metav1.Now(),
	}}
	if p.Update(event.UpdateEvent{ObjectOld: app, ObjectNew: churned}) {
		t.Error("condition-only update must be filtered")
	}

	// Routing-relevant update → passes.
	moved := app.DeepCopy()
	moved.Status.RouteTarget = "vibed-app-z.vibed-apps.svc.cluster.local:8080"
	if !p.Update(event.UpdateEvent{ObjectOld: app, ObjectNew: moved}) {
		t.Error("RouteTarget update must pass")
	}

	// Unexpected object type → fail open.
	if !p.Update(event.UpdateEvent{ObjectOld: &metav1.PartialObjectMetadata{}, ObjectNew: &metav1.PartialObjectMetadata{}}) {
		t.Error("non-VibedApp update must fail open (reconcile)")
	}
}

// quiet linter
var _ client.Object = &vibedv1.VibedApp{}
