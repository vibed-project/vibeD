package vibedhttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/policy"
	"github.com/vibed-project/vibeD/internal/tarball"
	"github.com/vibed-project/vibeD/internal/tenant"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// newDeployRouter wires a Server with a real deploy.Service (fake k8s client
// + served tarball store) behind a middleware that injects an authenticated
// owner — mirroring how main.go stacks auth in front of the API.
func newDeployRouter(t *testing.T, owner string, gates ...policy.Gate) (http.Handler, client.Client) {
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
		t.Fatalf("store: %v", err)
	}

	srv := New(nil, nil, nil, nil)
	srv.Deploy = &deploy.Service{
		Client:        c,
		Store:         store,
		Classifier:    classifier.Classifier{},
		Namespace:     "vibed-apps",
		DeployTimeout: time.Second,
		PollInterval:  10 * time.Millisecond,
	}
	if len(gates) > 0 {
		srv.Deploy.Policy = gates[0]
	}

	mux := http.NewServeMux()
	HandlerFromMux(srv, mux)

	// Inject the owner into the request context the way the auth middleware would.
	withOwner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(vibedauth.WithUserID(r.Context(), owner)))
	})
	return withOwner, c
}

func multipartDeploy(t *testing.T, name string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	meta, _ := json.Marshal(DeployMetadata{Name: name})
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

func markReady(c client.Client, name string, after time.Duration) {
	go func() {
		time.Sleep(after)
		app := &vibedv1.VibedApp{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "vibed-apps"}, app); err != nil {
			return
		}
		app.Status.Phase = vibedv1.PhaseReady
		app.Status.URL = "https://abc123def456.vibed.test"
		_ = c.Status().Update(context.Background(), app)
	}()
}

func TestDeployEndpoint200WhenReady(t *testing.T) {
	h, c := newDeployRouter(t, "alice@example.com")
	markReady(c, "myapp", 30*time.Millisecond)

	body, ct := multipartDeploy(t, "myapp", map[string]string{"go.mod": "module x\n", "main.go": "package main"})
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp DeployResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.AppId != "myapp" || resp.Url == nil || *resp.Url == "" {
		t.Errorf("unexpected response: %+v", resp)
	}

	// Created CR carries the classifier's pick (go.mod -> go-123).
	app := &vibedv1.VibedApp{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: "myapp", Namespace: "vibed-apps"}, app)
	if app.Spec.Runtime.Template != "go-123" {
		t.Errorf("template = %q, want go-123", app.Spec.Runtime.Template)
	}
}

func TestDeployEndpoint202WhenPending(t *testing.T) {
	h, _ := newDeployRouter(t, "alice@example.com")
	// no markReady → stays Claiming past the 1s budget... shorten via the
	// service's own timeout (set to 1s; we accept the wait).
	body, ct := multipartDeploy(t, "pending", map[string]string{"index.html": "<h1>x</h1>"})
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp DeployResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StatusUrl == nil || *resp.StatusUrl != "/v1/apps/pending" {
		t.Errorf("expected status_url /v1/apps/pending, got %+v", resp)
	}
}

type denyGate struct{}

func (denyGate) Evaluate(context.Context, policy.Input) error {
	return &policy.DeniedError{Reason: "template not allowed"}
}

func TestDeployEndpoint403WhenPolicyDenies(t *testing.T) {
	h, _ := newDeployRouter(t, "alice@example.com", denyGate{})
	body, ct := multipartDeploy(t, "blocked", map[string]string{"index.html": "<h1>x</h1>"})
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "policy_denied") {
		t.Errorf("body missing policy_denied code: %s", rec.Body.String())
	}
}

func TestDeployEndpoint401WithoutOwner(t *testing.T) {
	// Router with empty owner → unauthenticated.
	h, _ := newDeployRouter(t, "")
	body, ct := multipartDeploy(t, "noauth", map[string]string{"a": "b"})
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestGetAndListAndDelete(t *testing.T) {
	h, c := newDeployRouter(t, "alice@example.com")
	// Seed a ready app directly.
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "seeded", Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: "alice@example.com", Runtime: vibedv1.Runtime{Lane: vibedv1.LaneGeneral, Template: "node-24"}},
	}
	if err := c.Create(context.Background(), app); err != nil {
		t.Fatal(err)
	}

	// GET /v1/apps/seeded
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/seeded", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got App
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.AppId != "seeded" || got.Runtime == nil || got.Runtime.Template == nil || *got.Runtime.Template != "node-24" {
		t.Errorf("unexpected app: %+v", got)
	}

	// GET /v1/apps (list)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var list struct {
		Items []App `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].AppId != "seeded" {
		t.Errorf("list = %+v", list.Items)
	}

	// DELETE /v1/apps/seeded
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/apps/seeded", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
	// Now gone.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/seeded", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("after delete, get status = %d, want 404", rec.Code)
	}
}

func TestRedeployUnknownApp404(t *testing.T) {
	h, _ := newDeployRouter(t, "alice")
	body, ct := multipartDeploy(t, "ignored", map[string]string{"index.html": "<h1>x</h1>"})
	req := httptest.NewRequest(http.MethodPost, "/v1/apps/ghost/redeploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("redeploy unknown app = %d, want 404", rec.Code)
	}
}

func TestRedeployOwnedApp(t *testing.T) {
	h, c := newDeployRouter(t, "alice")
	if err := c.Create(context.Background(), &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: "alice"},
	}); err != nil {
		t.Fatal(err)
	}
	body, ct := multipartDeploy(t, "ignored", map[string]string{"index.html": "<h1>x</h1>"})
	req := httptest.NewRequest(http.MethodPost, "/v1/apps/mine/redeploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// No markReady, so the deploy is still pending: 202 (or 200 if it raced Ready).
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("redeploy owned app = %d, want 200/202", rec.Code)
	}
}

func TestRedeployHidesOtherOwnersApp(t *testing.T) {
	h, c := newDeployRouter(t, "alice")
	if err := c.Create(context.Background(), &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "bobs", Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: "bob"},
	}); err != nil {
		t.Fatal(err)
	}
	body, ct := multipartDeploy(t, "ignored", map[string]string{"index.html": "<h1>x</h1>"})
	req := httptest.NewRequest(http.MethodPost, "/v1/apps/bobs/redeploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("redeploy of another owner's app = %d, want 404 (not 403, to avoid leaking existence)", rec.Code)
	}
}

func TestStreamLogsUnknownApp404(t *testing.T) {
	h, _ := newDeployRouter(t, "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/ghost/logs", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("logs for unknown app = %d, want 404", rec.Code)
	}
}

func TestStreamLogsSSE(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := vibedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: "alice"},
		Status:     vibedv1.VibedAppStatus{PodIP: "10.0.0.9"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mine-pod", Namespace: "vibed-apps",
			Labels: map[string]string{"agents.x-k8s.io/claim-uid": "u1"},
		},
		Status: corev1.PodStatus{PodIP: "10.0.0.9"},
	}

	srv := New(nil, nil, nil, nil)
	srv.Deploy = &deploy.Service{Client: c, Clientset: k8sfake.NewSimpleClientset(pod), Namespace: "vibed-apps"}
	mux := http.NewServeMux()
	HandlerFromMux(srv, mux)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(vibedauth.WithUserID(r.Context(), "alice")))
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/mine/logs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("logs status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("expected SSE data frames, got: %q", rec.Body.String())
	}
}

// quotaDenied is a Service.Quota that always rejects new deploys with a
// QuotaExceeded()-compatible error, mirroring quota.ExceededError without
// pulling the internal package in.
type quotaDenied struct{}

func (quotaDenied) Authorize(context.Context, tenant.Tenant, string, bool) (string, error) {
	return "", quotaErr{}
}

type quotaErr struct{}

func (quotaErr) Error() string       { return "owner \"alice\" quota exceeded: 5/5 apps already deployed" }
func (quotaErr) QuotaExceeded() bool { return true }

// TestDeployEndpointReturns429OnQuotaExceeded locks in the contract the CLI
// and clients rely on: a quota rejection at the deploy path must surface as
// HTTP 429 with the enforcer's message in the body, not the default 400 the
// generic deploy-failed branch would emit.
func TestDeployEndpointReturns429OnQuotaExceeded(t *testing.T) {
	h, _ := newDeployRouter(t, "alice@example.com")
	// newDeployRouter doesn't expose the Service struct directly, so reuse
	// the deploy package by stripping the Quota field off the constructed
	// service via the same wiring path. We do that by building our own
	// router instead — the shared helper isn't extensible enough.
	srv := New(nil, nil, nil, nil)
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
		t.Fatalf("store: %v", err)
	}
	srv.Deploy = &deploy.Service{
		Client:        c,
		Store:         store,
		Classifier:    classifier.Classifier{},
		Namespace:     "vibed-apps",
		DeployTimeout: time.Second,
		PollInterval:  10 * time.Millisecond,
		Quota:         quotaDenied{},
	}
	mux := http.NewServeMux()
	HandlerFromMux(srv, mux)
	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(vibedauth.WithUserID(r.Context(), "alice@example.com")))
	})

	body, ct := multipartDeploy(t, "myapp", map[string]string{"go.mod": "module x\n"})
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	var resp Error
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Code != "quota_exceeded" {
		t.Errorf("code = %q, want quota_exceeded", resp.Code)
	}
	if !strings.Contains(resp.Message, "quota exceeded") {
		t.Errorf("message %q must include the enforcer's reason", resp.Message)
	}

	// And the rejection must NOT have created a VibedApp.
	got := &vibedv1.VibedApp{}
	gerr := c.Get(context.Background(), types.NamespacedName{Name: "myapp", Namespace: "vibed-apps"}, got)
	if gerr == nil {
		t.Error("VibedApp was created despite quota rejection")
	}
}

func TestLogStreamCapBlocksOverLimit(t *testing.T) {
	// Unit-level: the cap is the gatekeeper; verify Acquire/Release semantics
	// directly without spinning up SSE handlers (which finish synchronously
	// under the fake clientset and never sit on a slot long enough to test).
	srv := &Server{MaxConcurrentLogStreamsPerUser: 2}

	if !srv.acquireLogStream("alice") {
		t.Fatal("first stream must succeed")
	}
	if !srv.acquireLogStream("alice") {
		t.Fatal("second stream must succeed (at cap, not over)")
	}
	if srv.acquireLogStream("alice") {
		t.Fatal("third stream must be rejected — over per-user cap")
	}
	// Cap is per-user: a different user is unaffected.
	if !srv.acquireLogStream("bob") {
		t.Fatal("bob's first stream must succeed (different user)")
	}
	// Releasing alice's slot frees one for her.
	srv.releaseLogStream("alice")
	if !srv.acquireLogStream("alice") {
		t.Fatal("after release, alice must regain a slot")
	}
}

func TestLogStreamCapDisabled(t *testing.T) {
	srv := &Server{MaxConcurrentLogStreamsPerUser: 0} // 0 = unlimited
	for i := 0; i < 100; i++ {
		if !srv.acquireLogStream("alice") {
			t.Fatalf("acquire %d failed with cap disabled", i)
		}
	}
}

// TestLogStreamCapReturns429OverHTTP exercises the rejection through the
// real handler chain: a slot held by an existing stream causes the next
// /v1/apps/{id}/logs request to return 429 with a Retry-After header that
// clients use for exponential backoff. Holds the slot via acquireLogStream
// directly (in-package test) so we don't need a goroutine driving a blocking
// SSE stream — the cap check fires before the streaming code path.
func TestLogStreamCapReturns429OverHTTP(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := vibedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	app := &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: "alice"},
		Status:     vibedv1.VibedAppStatus{PodIP: "10.0.0.9"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mine-pod", Namespace: "vibed-apps",
			Labels: map[string]string{"agents.x-k8s.io/claim-uid": "u1"},
		},
		Status: corev1.PodStatus{PodIP: "10.0.0.9"},
	}

	srv := New(nil, nil, nil, nil)
	srv.Deploy = &deploy.Service{Client: c, Clientset: k8sfake.NewSimpleClientset(pod), Namespace: "vibed-apps"}
	srv.MaxConcurrentLogStreamsPerUser = 1
	mux := http.NewServeMux()
	HandlerFromMux(srv, mux)
	withAlice := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(vibedauth.WithUserID(r.Context(), "alice")))
	})

	// Pre-occupy alice's only slot so the next HTTP request must be rejected.
	if !srv.acquireLogStream("alice") {
		t.Fatal("test setup: failed to occupy alice's slot")
	}
	defer srv.releaseLogStream("alice")

	rec := httptest.NewRecorder()
	withAlice.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/mine/logs", nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Error("Retry-After header missing on 429 — clients rely on it for backoff")
	}
	var resp Error
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Code != "too_many_log_streams" {
		t.Errorf("code = %q, want too_many_log_streams", resp.Code)
	}

	// Bob is unaffected (per-user cap, not global).
	withBob := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(vibedauth.WithUserID(r.Context(), "bob")))
	})
	rec = httptest.NewRecorder()
	// Bob owns no apps, so this 404s, but the IMPORTANT check is it's NOT 429.
	withBob.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/apps/mine/logs", nil))
	if rec.Code == http.StatusTooManyRequests {
		t.Errorf("bob got 429 despite a separate per-user counter; status=%d", rec.Code)
	}
}

func TestSuspendAndResumeEndpoints(t *testing.T) {
	h, c := newDeployRouter(t, "alice")
	if err := c.Create(context.Background(), &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "vibed-apps"},
		Spec:       vibedv1.VibedAppSpec{Owner: "alice"},
	}); err != nil {
		t.Fatal(err)
	}

	// Suspend -> 202, spec.suspended=true.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/apps/mine/suspend", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("suspend = %d, want 202", rec.Code)
	}
	got := &vibedv1.VibedApp{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: "mine", Namespace: "vibed-apps"}, got)
	if !got.Spec.Suspended {
		t.Error("expected spec.suspended=true after suspend endpoint")
	}

	// Resume -> 202, spec.suspended=false.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/apps/mine/resume", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("resume = %d, want 202", rec.Code)
	}
	got = &vibedv1.VibedApp{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: "mine", Namespace: "vibed-apps"}, got)
	if got.Spec.Suspended {
		t.Error("expected spec.suspended=false after resume endpoint")
	}
}

func TestSuspendUnknownApp404(t *testing.T) {
	h, _ := newDeployRouter(t, "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/apps/ghost/suspend", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("suspend unknown app = %d, want 404", rec.Code)
	}
}
