package router

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

// quiet linter
var _ client.Object = &vibedv1.VibedApp{}
