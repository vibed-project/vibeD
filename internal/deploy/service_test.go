package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/audit"
	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/meter"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/policy"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/internal/tarball"
	"github.com/vibed-project/vibeD/internal/tenant"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// fakeStore is an in-memory tarball.Store for tests.
type fakeStore struct {
	mu       sync.Mutex
	blobs    map[string][]byte
	putCount int
	delCount int
}

func newFakeStore() *fakeStore { return &fakeStore{blobs: map[string][]byte{}} }

func (f *fakeStore) Put(_ context.Context, id string, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[id] = b
	f.putCount++
	return "http://store.test/" + id + ".tar.gz", nil
}

func (f *fakeStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blobs, id)
	f.delCount++
	return nil
}

func (f *fakeStore) puts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.putCount
}

// compile-time: fakeStore is a real tarball.Store.
var _ tarball.Store = (*fakeStore)(nil)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := vibedv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func gzTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func newService(c client.Client, store tarball.Store) *Service {
	return &Service{
		Client:        c,
		Store:         store,
		Classifier:    classifier.Classifier{},
		Namespace:     "vibed-apps",
		DeployTimeout: 2 * time.Second,
		PollInterval:  10 * time.Millisecond,
	}
}

// markReady flips the VibedApp to Ready in the fake client after a short
// delay, simulating the controller's reconcile.
func markReady(c client.Client, name, ns string, after time.Duration) {
	go func() {
		time.Sleep(after)
		app := &vibedv1.VibedApp{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, app); err != nil {
			return
		}
		app.Status.Phase = vibedv1.PhaseReady
		app.Status.URL = "https://abc123def456.vibed.example.com"
		_ = c.Status().Update(context.Background(), app)
	}()
}

func TestDeployReachesReady(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	store := newFakeStore()
	svc := newService(c, store)

	markReady(c, "hello", "vibed-apps", 30*time.Millisecond)

	res, err := svc.Deploy(context.Background(), Request{
		Name:    "hello",
		Owner:   "alice@example.com",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"requirements.txt": "flask", "app.py": "x"})),
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !res.Ready || res.Phase != vibedv1.PhaseReady {
		t.Fatalf("expected Ready, got %+v", res)
	}
	if res.URL == "" {
		t.Error("expected URL on Ready")
	}

	app := &vibedv1.VibedApp{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "hello", Namespace: "vibed-apps"}, app); err != nil {
		t.Fatalf("get created app: %v", err)
	}
	if app.Spec.Runtime.Template != "python-313" {
		t.Errorf("template = %q, want python-313", app.Spec.Runtime.Template)
	}
	if app.Spec.Owner != "alice@example.com" {
		t.Errorf("owner = %q", app.Spec.Owner)
	}
	if app.Spec.Source.TarballRef == "" {
		t.Error("expected source.tarballRef to be set from the store")
	}
	if store.puts() != 1 {
		t.Errorf("expected 1 tarball put, got %d", store.puts())
	}
}

func TestDeployAndDeleteRecordMetrics(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.Metrics = metrics.New()
	const tmpl = "static-nginx" // index.html classifies to the static fast lane

	deploysBefore := testutil.ToFloat64(svc.Metrics.DeploysTotal.WithLabelValues("success", tmpl))
	activeBefore := testutil.ToFloat64(svc.Metrics.ArtifactsActive.WithLabelValues(tmpl))

	if _, err := svc.Deploy(context.Background(), Request{
		Name:    "metricsapp",
		Owner:   "alice@example.com",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "<h1>hi</h1>"})),
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if d := testutil.ToFloat64(svc.Metrics.DeploysTotal.WithLabelValues("success", tmpl)) - deploysBefore; d != 1 {
		t.Fatalf("deploys_total{success,%s} delta = %v, want 1", tmpl, d)
	}
	if d := testutil.ToFloat64(svc.Metrics.ArtifactsActive.WithLabelValues(tmpl)) - activeBefore; d != 1 {
		t.Fatalf("artifacts_active{%s} delta after deploy = %v, want 1", tmpl, d)
	}

	// Delete balances the live-apps gauge back to where it started.
	if err := svc.Delete(context.Background(), "alice@example.com", "metricsapp"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if d := testutil.ToFloat64(svc.Metrics.ArtifactsActive.WithLabelValues(tmpl)) - activeBefore; d != 0 {
		t.Fatalf("artifacts_active{%s} delta after delete = %v, want 0", tmpl, d)
	}
}

func TestDeployTimesOutToPending(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.DeployTimeout = 80 * time.Millisecond
	// Never mark Ready → Deploy returns not-ready (the 202 path).

	res, err := svc.Deploy(context.Background(), Request{
		Name:    "slow",
		Owner:   "bob@example.com",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "<h1>hi</h1>"})),
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res.Ready {
		t.Error("expected not-ready on timeout")
	}
	if res.AppID != "slow" {
		t.Errorf("app id = %q", res.AppID)
	}
}

func TestVersionsAndRollback(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.DeployTimeout = 50 * time.Millisecond // return pending fast; versions record pre-wait

	dep := func(content string) {
		if _, err := svc.Deploy(context.Background(), Request{
			Name: "verapp", Owner: "alice@example.com",
			Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": content})),
		}); err != nil {
			t.Fatalf("deploy: %v", err)
		}
	}
	dep("<h1>v1</h1>")
	dep("<h1>v2</h1>")

	versions, err := svc.Versions(context.Background(), "alice@example.com", "verapp")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("versions wrong (want [v2 v1]): %+v", versions)
	}
	if versions[0].TarballKey == versions[1].TarballKey {
		t.Fatal("each version must retain a distinct source tarball")
	}

	// Roll back to v1 → a new version 3 that reuses v1's retained source.
	if _, err := svc.Rollback(context.Background(), "alice@example.com", "verapp", 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	versions, _ = svc.Versions(context.Background(), "alice@example.com", "verapp")
	if len(versions) != 3 || versions[0].Version != 3 || versions[0].RolledBackFrom != 1 {
		t.Fatalf("rollback version wrong: %+v", versions)
	}
	app := &vibedv1.VibedApp{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "verapp", Namespace: "vibed-apps"}, app); err != nil {
		t.Fatal(err)
	}
	if app.Spec.Source.TarballRef != versions[2].TarballRef { // versions[2] == v1
		t.Fatalf("rollback did not repoint source to v1")
	}

	if _, err := svc.Rollback(context.Background(), "alice@example.com", "verapp", 99); err == nil {
		t.Fatal("expected an error rolling back to an unknown version")
	}
}

func TestDeployRejectsBadName(t *testing.T) {
	svc := newService(fake.NewClientBuilder().WithScheme(newScheme(t)).Build(), newFakeStore())
	_, err := svc.Deploy(context.Background(), Request{
		Name:    "Bad_Name",
		Owner:   "x",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"a": "b"})),
	})
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
}

func TestDeployHonorsOverride(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	markReady(c, "override", "vibed-apps", 20*time.Millisecond)

	_, err := svc.Deploy(context.Background(), Request{
		Name:             "override",
		Owner:            "carol@example.com",
		Tarball:          bytes.NewReader(gzTarball(t, map[string]string{"index.html": "<h1>static</h1>"})),
		LaneOverride:     vibedv1.LaneGeneral,
		TemplateOverride: "base-al2023",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	app := &vibedv1.VibedApp{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: "override", Namespace: "vibed-apps"}, app)
	if app.Spec.Runtime.Template != "base-al2023" || app.Spec.Runtime.Lane != vibedv1.LaneGeneral {
		t.Errorf("override not honored: lane=%q template=%q", app.Spec.Runtime.Lane, app.Spec.Runtime.Template)
	}
}

// ownedApp builds a VibedApp with the vibed.dev/owner label the deploy path
// stamps, so tests exercising the label-filtered List behave like production.
func ownedApp(name, namespace, owner string) *vibedv1.VibedApp {
	return &vibedv1.VibedApp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{vibedv1.LabelOwner: vibedv1.SanitizeLabel(owner)},
		},
		Spec: vibedv1.VibedAppSpec{Owner: owner},
	}
}

func TestGetListDeleteOwnership(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(
			// Apps carry the vibed.dev/owner label the deploy path stamps, so
			// List's server-side owner pre-filter (#74) selects them.
			ownedApp("a", "vibed-apps", "alice"),
			ownedApp("b", "vibed-apps", "bob"),
		).Build()
	svc := newService(c, newFakeStore())

	list, err := svc.List(context.Background(), "alice")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "a" {
		t.Errorf("alice's list = %+v, want [a]", list)
	}

	if _, err := svc.Get(context.Background(), "alice", "b"); err != ErrNotFound {
		t.Errorf("cross-owner Get should be ErrNotFound, got %v", err)
	}

	if err := svc.Delete(context.Background(), "alice", "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(context.Background(), "alice", "a"); err != ErrNotFound {
		t.Errorf("app should be gone after delete")
	}
}

// ---- quota + audit wiring ----

// A configured tenant resolver routes the deploy to the tenant's namespace,
// stamps the tenant label, and scopes Get/List — the multi-tenant path.
func TestDeployRoutesToTenantNamespace(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.Tenants = tenant.Single(tenant.Tenant{ID: "acme", Namespace: "tenant-acme"})

	markReady(c, "app1", "tenant-acme", 30*time.Millisecond)
	res, err := svc.Deploy(context.Background(), Request{
		Name:    "app1",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "<html></html>"})),
	})
	if err != nil || !res.Ready {
		t.Fatalf("Deploy: %v, %+v", err, res)
	}

	// Created in the tenant namespace with the tenant label...
	app := &vibedv1.VibedApp{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app1", Namespace: "tenant-acme"}, app); err != nil {
		t.Fatalf("app not in tenant namespace: %v", err)
	}
	if app.Labels[vibedv1.LabelTenant] != "acme" {
		t.Errorf("tenant label = %q, want acme", app.Labels[vibedv1.LabelTenant])
	}
	// ...and NOT in the default namespace.
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app1", Namespace: "vibed-apps"}, &vibedv1.VibedApp{}); err == nil {
		t.Error("app must not exist in the default namespace")
	}
	// Get/List are scoped to the tenant namespace.
	if got, err := svc.Get(context.Background(), "alice", "app1"); err != nil || got.Namespace != "tenant-acme" {
		t.Fatalf("Get in tenant: %v %+v", err, got)
	}
	if list, err := svc.List(context.Background(), "alice"); err != nil || len(list) != 1 {
		t.Fatalf("List in tenant: %v n=%d", err, len(list))
	}
}

// A named tenant with no namespace is rejected (fail closed), rather than
// silently falling back to the shared default namespace.
func TestDeployRejectsNamedTenantWithoutNamespace(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.Tenants = tenant.Single(tenant.Tenant{ID: "acme"}) // ID set, Namespace empty

	_, err := svc.Deploy(context.Background(), Request{
		Name:    "app1",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "x"})),
	})
	if err == nil || !strings.Contains(err.Error(), "without a namespace") {
		t.Fatalf("want a 'without a namespace' error, got %v", err)
	}
}

type denyPolicy struct{}

func (denyPolicy) Evaluate(context.Context, policy.Input) error {
	return &policy.DeniedError{Reason: "not allowed"}
}

type recordingMeter struct{ events []meter.Event }

func (m *recordingMeter) Record(_ context.Context, e meter.Event) { m.events = append(m.events, e) }

// A Policy that denies aborts the deploy (403-tagged error) and no CR is created.
func TestDeployPolicyDenies(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.Policy = denyPolicy{}

	_, err := svc.Deploy(context.Background(), Request{
		Name:    "app1",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "x"})),
	})
	var pe interface{ PolicyDenied() bool }
	if !errors.As(err, &pe) || !pe.PolicyDenied() {
		t.Fatalf("want a PolicyDenied error, got %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app1", Namespace: "vibed-apps"}, &vibedv1.VibedApp{}); err == nil {
		t.Error("a policy-denied deploy must not create a CR")
	}
}

// The meter records a "deploy" then a "delete" usage event with owner/app.
func TestMeterRecordsDeployAndDelete(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	rec := &recordingMeter{}
	svc.Meter = rec

	markReady(c, "app1", "vibed-apps", 30*time.Millisecond)
	if _, err := svc.Deploy(context.Background(), Request{
		Name:    "app1",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "x"})),
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := svc.Delete(context.Background(), "alice", "app1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(rec.events) != 2 || rec.events[0].Kind != "deploy" || rec.events[1].Kind != "delete" {
		t.Fatalf("events = %+v", rec.events)
	}
	if rec.events[0].App != "app1" || rec.events[0].Owner != "alice" {
		t.Errorf("deploy event = %+v", rec.events[0])
	}
}

type fakeQuota struct {
	dept     string
	deny     bool
	lastNew  bool
	released int // times the returned release func was invoked
}

func (q *fakeQuota) Authorize(_ context.Context, _ tenant.Tenant, _ string, isNew bool) (string, func(), error) {
	q.lastNew = isNew
	release := func() { q.released++ }
	if q.deny {
		return q.dept, release, &fakeQuotaErr{}
	}
	return q.dept, release, nil
}

type fakeQuotaErr struct{}

func (e *fakeQuotaErr) Error() string       { return "quota exceeded" }
func (e *fakeQuotaErr) QuotaExceeded() bool { return true }

type fakeAuditor struct {
	mu     sync.Mutex
	events []string // "action:outcome"
}

func (a *fakeAuditor) Record(_ context.Context, action, _, outcome, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, action+":"+outcome)
	return nil
}

// failClosedAuditor models a fail-closed Recorder whose store has gone away:
// pre-action records (denied/error) are swallowed (the service is already
// returning an error), but success-path records bubble the error up.
type failClosedAuditor struct {
	mu     sync.Mutex
	events []string
	err    error // returned only when outcome == "ok"
}

func (a *failClosedAuditor) Record(_ context.Context, action, _, outcome, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, action+":"+outcome)
	if outcome == "ok" {
		return a.err
	}
	return nil
}

func (a *fakeAuditor) got() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.events...)
}

func TestDeployQuotaDeniesNewApp(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	store := newFakeStore()
	svc := newService(c, store)
	aud := &fakeAuditor{}
	svc.Quota = &fakeQuota{deny: true}
	svc.Audit = aud

	_, err := svc.Deploy(context.Background(), Request{
		Name:    "denied-app",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "x"})),
	})
	var qe interface{ QuotaExceeded() bool }
	if !errors.As(err, &qe) {
		t.Fatalf("want a quota error, got %v", err)
	}
	if store.puts() != 0 {
		t.Errorf("a rejected deploy must not store source, got %d puts", store.puts())
	}
	if got := aud.got(); len(got) != 1 || got[0] != "deploy:denied" {
		t.Errorf("audit = %v, want [deploy:denied]", got)
	}
}

func TestDeployStampsLabelsAndAudits(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	aud := &fakeAuditor{}
	svc.Quota = &fakeQuota{dept: "platform"}
	svc.Audit = aud
	markReady(c, "labeled", "vibed-apps", 20*time.Millisecond)

	if _, err := svc.Deploy(context.Background(), Request{
		Name:    "labeled",
		Owner:   "alice@x.io",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "x"})),
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	app := &vibedv1.VibedApp{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: "labeled", Namespace: "vibed-apps"}, app)
	if app.Labels[vibedv1.LabelOwner] != vibedv1.SanitizeLabel("alice@x.io") {
		t.Errorf("owner label = %q", app.Labels[vibedv1.LabelOwner])
	}
	if app.Labels[vibedv1.LabelDepartment] != "platform" {
		t.Errorf("department label = %q, want platform", app.Labels[vibedv1.LabelDepartment])
	}
	if got := aud.got(); len(got) != 1 || got[0] != "deploy:ok" {
		t.Errorf("audit = %v, want [deploy:ok]", got)
	}
}

// TestDeployEnrichesAuditWithTenantAndSourceHash proves the deploy path pushes
// the resolved tenant and the source tarball's content hash into the persisted
// audit event, and that Delete carries the tenant through as well. It wires a
// real audit.Recorder over a memory store so the enrichment is observed exactly
// as it lands in the audit trail.
func TestDeployEnrichesAuditWithTenantAndSourceHash(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	mem := store.NewMemoryStore()
	svc := newService(c, newFakeStore())
	svc.Audit = audit.New(mem, nil, false)
	svc.Tenants = tenant.Single(tenant.Tenant{ID: "acme", Namespace: "tenant-acme"})
	markReady(c, "app1", "tenant-acme", 20*time.Millisecond)

	// Capture the exact source bytes so we can assert the deploy path hashes the
	// whole tarball correctly — the hash is now computed in the read pass via a
	// TeeReader (#73), so a truncated or double-counted read would show here.
	tarball := gzTarball(t, map[string]string{"index.html": "x"})
	wantHash := fmt.Sprintf("%x", sha256.Sum256(tarball))

	if _, err := svc.Deploy(context.Background(), Request{
		Name:    "app1",
		Owner:   "alice",
		Tarball: bytes.NewReader(tarball),
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := svc.Delete(context.Background(), "alice", "app1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	events, err := mem.ListAudit(context.Background(), store.AuditQuery{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d audit events, want 2", len(events))
	}
	// Newest first: delete then deploy.
	del, dep := events[0], events[1]
	if del.Action != "delete" || del.TenantID != "acme" {
		t.Errorf("delete event = %+v, want tenant acme", del)
	}
	if dep.Action != "deploy" || dep.TenantID != "acme" {
		t.Errorf("deploy event = %+v, want tenant acme", dep)
	}
	if dep.SourceHash != wantHash {
		t.Errorf("deploy event SourceHash = %q, want sha256 of the tarball %q", dep.SourceHash, wantHash)
	}
	// Delete has no source, so no hash.
	if del.SourceHash != "" {
		t.Errorf("delete event should not carry a SourceHash, got %q", del.SourceHash)
	}
}

// TestDeployFailsClosedWhenAuditWriteFails locks in the fail-closed contract:
// when the post-success audit Record returns an error, Deploy surfaces that
// error to the caller. The VibedApp is already created and the controller
// will reconcile it; the API just tells the caller "we couldn't durably
// record this," so they retry (deploy is idempotent) or alert.
func TestDeployFailsClosedWhenAuditWriteFails(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	store := newFakeStore()
	svc := newService(c, store)
	aud := &failClosedAuditor{err: errors.New("audit store: disk full")}
	svc.Audit = aud
	markReady(c, "myapp", "vibed-apps", 20*time.Millisecond)

	_, err := svc.Deploy(context.Background(), Request{
		Name:    "myapp",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"go.mod": "module x\n"})),
	})
	if err == nil {
		t.Fatal("Deploy must return error when fail-closed audit write fails")
	}
	if !strings.Contains(err.Error(), "audit failed") {
		t.Errorf("error %q must mention audit failure", err.Error())
	}

	// Side-effect contract: the VibedApp WAS created (the deploy succeeded
	// pre-audit). The error tells the caller to retry/alert, not that the
	// workload is missing.
	got := &vibedv1.VibedApp{}
	if gerr := c.Get(context.Background(), types.NamespacedName{Name: "myapp", Namespace: "vibed-apps"}, got); gerr != nil {
		t.Fatalf("VibedApp should exist despite audit failure: %v", gerr)
	}

	// And the recorder saw exactly the success record (no pre-action error event).
	if want := []string{"deploy:ok"}; !reflect.DeepEqual(aud.events, want) {
		t.Errorf("audit events = %v, want %v", aud.events, want)
	}
}

// TestDeployPreActionAuditFailureIsSwallowed covers the inverse of fail-closed:
// when the deploy is already being denied (quota), the Recorder's error on
// that pre-action audit must NOT mask the original quota error.
func TestDeployPreActionAuditFailureIsSwallowed(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).Build()
	svc := newService(c, newFakeStore())
	svc.Quota = &fakeQuota{deny: true}
	// Force the audit Record to error on EVERY outcome — pre-action calls
	// must still swallow it, only the success path bubbles.
	failAll := &failClosedAuditor{err: errors.New("store down")}
	failAll.events = nil
	failAll.err = errors.New("store down")
	svc.Audit = &alwaysFailAuditor{}

	_, err := svc.Deploy(context.Background(), Request{
		Name:    "myapp",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"go.mod": "module x\n"})),
	})
	if err == nil {
		t.Fatal("expected the quota-exceeded error to propagate")
	}
	var qe interface{ QuotaExceeded() bool }
	if !errors.As(err, &qe) {
		t.Errorf("error %q does not satisfy QuotaExceeded() — pre-action audit failure masked the original cause", err.Error())
	}
}

// alwaysFailAuditor errors on every Record call, regardless of outcome.
type alwaysFailAuditor struct{}

func (alwaysFailAuditor) Record(context.Context, string, string, string, string) error {
	return errors.New("audit store down")
}

func TestRedeployPassesIsNewFalse(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(&vibedv1.VibedApp{
			ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "vibed-apps"},
			Spec:       vibedv1.VibedAppSpec{Owner: "alice"},
		}).Build()
	svc := newService(c, newFakeStore())
	q := &fakeQuota{}
	svc.Quota = q
	markReady(c, "existing", "vibed-apps", 20*time.Millisecond)

	if _, err := svc.Deploy(context.Background(), Request{
		Name:    "existing",
		Owner:   "alice",
		Tarball: bytes.NewReader(gzTarball(t, map[string]string{"index.html": "x"})),
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if q.lastNew {
		t.Error("a redeploy must pass isNew=false to the quota enforcer")
	}
}

func TestSetSuspendedTogglesAndAudits(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(&vibedv1.VibedApp{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "vibed-apps"},
			Spec:       vibedv1.VibedAppSpec{Owner: "alice"},
		}).Build()
	svc := newService(c, newFakeStore())
	aud := &fakeAuditor{}
	svc.Audit = aud

	app, err := svc.SetSuspended(context.Background(), "alice", "a", true)
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if !app.Spec.Suspended {
		t.Error("expected spec.suspended=true after suspend")
	}

	app, err = svc.SetSuspended(context.Background(), "alice", "a", false)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if app.Spec.Suspended {
		t.Error("expected spec.suspended=false after resume")
	}

	if got := aud.got(); len(got) != 2 || got[0] != "suspend:ok" || got[1] != "resume:ok" {
		t.Errorf("audit = %v, want [suspend:ok resume:ok]", got)
	}
}

func TestSetSuspendedOwnership(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(&vibedv1.VibedApp{
			ObjectMeta: metav1.ObjectMeta{Name: "bobs", Namespace: "vibed-apps"},
			Spec:       vibedv1.VibedAppSpec{Owner: "bob"},
		}).Build()
	svc := newService(c, newFakeStore())

	if _, err := svc.SetSuspended(context.Background(), "alice", "bobs", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner SetSuspended = %v, want ErrNotFound", err)
	}
}
