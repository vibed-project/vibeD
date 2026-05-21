// Package e2e contains end-to-end tests for vibeD.
//
// slice_test.go is an IN-PROCESS E2E: it wires the real components together
// (the /v1 HTTP server, the deploy service, the classifier, the controller
// reconciler, and the router) against a controller-runtime fake API and an
// httptest fake Caddy. A "reconcile pump" goroutine stands in for the
// controller-manager, repeatedly reconciling VibedApps the way a real manager
// would. agent-sandbox binding and the vibed-agent probe are simulated by the
// controller's Dummy* implementations.
//
// This exercises the full vertical slice that's implemented today without
// needing a Kubernetes cluster, Kata, workerd, or built images — so it runs
// in plain `go test`. The literal cluster E2E (refactor.md §10.2) lives in
// cluster_test.go behind a build tag.
package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/config"
	vibedctrl "github.com/vibed-project/vibeD/internal/controller"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/router"
	"github.com/vibed-project/vibeD/internal/tarball"
	vibedhttp "github.com/vibed-project/vibeD/pkg/vibedapi/http"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

const (
	appsNS = "vibed-apps"
	domain = "vibed.example.com"
	owner  = "alice@example.com"
)

// harness holds the wired-together system under test.
type harness struct {
	http     http.Handler
	client   client.Client
	router   *router.Reconciler
	caddy    *fakeCaddy
	stopPump func()
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := vibedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&vibedv1.VibedApp{}).Build()

	store, err := tarball.New(config.TarballConfig{
		Backend: "served",
		Served:  config.ServedTarballConfig{BasePath: t.TempDir(), PublicBaseURL: "http://vibed.test"},
	})
	if err != nil {
		t.Fatalf("tarball store: %v", err)
	}

	// Real /v1 server + deploy service.
	deploySvc := &deploy.Service{
		Client:        c,
		Store:         store,
		Classifier:    classifier.Classifier{},
		Namespace:     appsNS,
		DeployTimeout: 5 * time.Second,
		PollInterval:  10 * time.Millisecond,
	}
	srv := vibedhttp.New(nil, nil, nil, nil)
	srv.Deploy = deploySvc
	mux := http.NewServeMux()
	vibedhttp.HandlerFromMux(srv, mux)
	authed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(vibedauth.WithUserID(r.Context(), owner)))
	})

	// Real controller reconciler with simulated agent-sandbox + a fake fast lane.
	rec := &vibedctrl.Reconciler{
		Client:       c,
		Scheme:       scheme,
		Claimer:      vibedctrl.DummyClaimer{}, // binds immediately, podIP 10.0.0.1
		Probe:        vibedctrl.DummyAgentProbe{},
		Router:       vibedctrl.DeterministicRouter{Domain: domain},
		FastLane:     e2eFastLane{target: "vibed-workerd-0.vibed-workerd.vibed-system.svc:9137"},
		RequeueDelay: 5 * time.Millisecond,
	}

	// Reconcile pump: stands in for the controller-manager.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				var list vibedv1.VibedAppList
				if err := c.List(ctx, &list); err != nil {
					continue
				}
				for i := range list.Items {
					a := &list.Items[i]
					_, _ = rec.Reconcile(ctx, reconcile.Request{
						NamespacedName: types.NamespacedName{Name: a.Name, Namespace: a.Namespace},
					})
				}
			}
		}
	}()

	caddy := newFakeCaddy()
	caddySrv := httptest.NewServer(caddy.handler())

	rr := &router.Reconciler{
		Client:  c,
		Scheme:  scheme,
		Caddy:   router.NewCaddyClient(caddySrv.URL),
		AppPort: 8080,
	}

	t.Cleanup(func() {
		cancel()
		wg.Wait()
		caddySrv.Close()
	})

	return &harness{http: authed, client: c, router: rr, caddy: caddy, stopPump: cancel}
}

// e2eFastLane is a stand-in workerd fast-lane deployer.
type e2eFastLane struct{ target string }

func (f e2eFastLane) Deploy(_ context.Context, _ *vibedv1.VibedApp) (string, error) {
	return f.target, nil
}
func (f e2eFastLane) Remove(_ context.Context, _ *vibedv1.VibedApp) error { return nil }

// TestSlicePerTemplate deploys one fixture per classifier destination and
// drives it all the way through to a programmed Caddy route, then tears it
// down. This is the closest thing to "deploy each fixture, assert a URL
// responds" (refactor.md §10.2) that runs without a cluster.
func TestSlicePerTemplate(t *testing.T) {
	cases := []struct {
		name         string
		files        map[string]string
		wantTemplate string
		wantLane     vibedv1.Lane
		fastLane     bool // routes via workerd (host:port upstream) vs sandbox pod (podIP:8080)
	}{
		{"static", map[string]string{"index.html": "<h1>hi</h1>"}, "static-nginx", vibedv1.LaneFast, false},
		{"node", map[string]string{"package.json": `{"dependencies":{"express":"^4"}}`, "index.js": "x"}, "node-24", vibedv1.LaneGeneral, false},
		{"python", map[string]string{"requirements.txt": "flask", "app.py": "x"}, "python-313", vibedv1.LaneGeneral, false},
		{"goapp", map[string]string{"go.mod": "module x\ngo 1.23\n", "main.go": "package main"}, "go-123", vibedv1.LaneGeneral, false},
		{"dockerfile", map[string]string{"Dockerfile": "FROM scratch", "bin": "x"}, "base-al2023", vibedv1.LaneGeneral, false},
		{"worker", map[string]string{"worker.js": "export default {}"}, "workerd", vibedv1.LaneFast, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			// 1. Deploy over the real /v1 HTTP API.
			body, ct := multipartDeploy(t, tc.name, tc.files)
			req := httptest.NewRequest(http.MethodPost, "/v1/deploy", body)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			h.http.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
				t.Fatalf("deploy status = %d (want 200/202); body=%s", rec.Code, rec.Body.String())
			}
			var dr vibedhttp.DeployResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &dr); err != nil {
				t.Fatalf("decode deploy response: %v", err)
			}
			if dr.AppId != tc.name {
				t.Fatalf("app_id = %q, want %q", dr.AppId, tc.name)
			}

			// 2. The classifier's pick landed on the CR.
			app := &vibedv1.VibedApp{}
			if err := h.client.Get(context.Background(), types.NamespacedName{Name: tc.name, Namespace: appsNS}, app); err != nil {
				t.Fatalf("get VibedApp: %v", err)
			}
			if app.Spec.Runtime.Template != tc.wantTemplate {
				t.Errorf("template = %q, want %q", app.Spec.Runtime.Template, tc.wantTemplate)
			}
			if app.Spec.Runtime.Lane != tc.wantLane {
				t.Errorf("lane = %q, want %q", app.Spec.Runtime.Lane, tc.wantLane)
			}

			// 3. The pump drives it to Ready with a URL.
			ready := waitReady(t, h.client, tc.name)
			if ready.Status.URL == "" {
				t.Fatal("expected a URL once Ready")
			}

			// 4. The router programs Caddy.
			reconcileRouter(t, h.router, tc.name)
			routeID := router.RoutePrefix + vibedctrl.AppLabel(ready)
			rt, ok := h.caddy.route(routeID)
			if !ok {
				t.Fatalf("router did not program a Caddy route for %s", routeID)
			}
			wantHost := vibedctrl.AppLabel(ready) + "." + domain
			if !bytes.Contains(rt, []byte(wantHost)) {
				t.Errorf("route missing host %s: %s", wantHost, rt)
			}
			// Fast-lane (workerd) upstreams carry a port; sandbox upstreams are podIP:8080.
			wantUpstream := "10.0.0.1:8080"
			if tc.fastLane {
				wantUpstream = "vibed-workerd-0.vibed-workerd.vibed-system.svc:9137"
			}
			if !bytes.Contains(rt, []byte(wantUpstream)) {
				t.Errorf("route missing upstream %s: %s", wantUpstream, rt)
			}

			// 5. GET /v1/apps/{id} reflects Ready.
			grec := httptest.NewRecorder()
			h.http.ServeHTTP(grec, httptest.NewRequest(http.MethodGet, "/v1/apps/"+tc.name, nil))
			if grec.Code != http.StatusOK {
				t.Fatalf("get app status = %d", grec.Code)
			}
			var got vibedhttp.App
			_ = json.Unmarshal(grec.Body.Bytes(), &got)
			if got.Phase != vibedhttp.Phase(vibedv1.PhaseReady) {
				t.Errorf("phase = %q, want Ready", got.Phase)
			}

			// 6. DELETE removes the app, and the router drops its route.
			drec := httptest.NewRecorder()
			h.http.ServeHTTP(drec, httptest.NewRequest(http.MethodDelete, "/v1/apps/"+tc.name, nil))
			if drec.Code != http.StatusNoContent {
				t.Fatalf("delete status = %d", drec.Code)
			}
			reconcileRouter(t, h.router, tc.name) // app gone -> route removed
			if _, ok := h.caddy.route(routeID); ok {
				t.Errorf("route %s should be removed after delete", routeID)
			}
		})
	}
}

// TestListScopedToOwner is a small cross-cutting check that the /v1 list view
// is owner-scoped end to end.
func TestListScopedToOwner(t *testing.T) {
	h := newHarness(t)
	for _, n := range []string{"alpha", "beta"} {
		body, ct := multipartDeploy(t, n, map[string]string{"index.html": "<h1>x</h1>"})
		req := httptest.NewRequest(http.MethodPost, "/v1/deploy", body)
		req.Header.Set("Content-Type", ct)
		h.http.ServeHTTP(httptest.NewRecorder(), req)
	}
	grec := httptest.NewRecorder()
	h.http.ServeHTTP(grec, httptest.NewRequest(http.MethodGet, "/v1/apps", nil))
	if grec.Code != http.StatusOK {
		t.Fatalf("list status = %d", grec.Code)
	}
	var list struct {
		Items []vibedhttp.App `json:"items"`
	}
	_ = json.Unmarshal(grec.Body.Bytes(), &list)
	if len(list.Items) != 2 {
		t.Errorf("owner should see 2 apps, got %d", len(list.Items))
	}
}

// --- helpers ---

func waitReady(t *testing.T, c client.Client, name string) *vibedv1.VibedApp {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		app := &vibedv1.VibedApp{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: appsNS}, app); err == nil {
			if app.Status.Phase == vibedv1.PhaseReady {
				return app
			}
			if app.Status.Phase == vibedv1.PhaseFailed {
				t.Fatalf("app %s went Failed: %+v", name, app.Status.Conditions)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("app %s never reached Ready", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func reconcileRouter(t *testing.T, rr *router.Reconciler, name string) {
	t.Helper()
	if _, err := rr.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: appsNS},
	}); err != nil {
		t.Fatalf("router reconcile: %v", err)
	}
}

func multipartDeploy(t *testing.T, name string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	meta, _ := json.Marshal(map[string]string{"name": name})
	_ = mw.WriteField("metadata", string(meta))
	fw, err := mw.CreateFormFile("source", "source.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(fw)
	tw := tar.NewWriter(gz)
	for n, c := range files {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(c))
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = mw.Close()
	return &body, mw.FormDataContentType()
}

// fakeCaddy is a minimal stand-in for the Caddy admin API: it records routes
// by @id and supports the PATCH-then-POST upsert + DELETE the router uses.
type fakeCaddy struct {
	mu     sync.Mutex
	routes map[string]json.RawMessage
}

func newFakeCaddy() *fakeCaddy { return &fakeCaddy{routes: map[string]json.RawMessage{}} }

func (f *fakeCaddy) route(id string) (json.RawMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.routes[id]
	return r, ok
}

func (f *fakeCaddy) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/id/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/id/"):]
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPatch:
			if _, ok := f.routes[id]; !ok {
				http.NotFound(w, r)
				return
			}
			body, _ := readAll(r)
			f.routes[id] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if _, ok := f.routes[id]; !ok {
				http.NotFound(w, r)
				return
			}
			delete(f.routes, id)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/config/apps/http/servers/srv0/routes/...", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := readAll(r)
		var probe struct {
			ID string `json:"@id"`
		}
		if err := json.Unmarshal(body, &probe); err != nil || probe.ID == "" {
			http.Error(w, "missing @id", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.routes[probe.ID] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func readAll(r *http.Request) (json.RawMessage, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}
