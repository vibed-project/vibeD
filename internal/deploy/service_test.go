package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/tarball"
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

func TestGetListDeleteOwnership(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&vibedv1.VibedApp{}).
		WithObjects(
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "alice"}},
			&vibedv1.VibedApp{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "vibed-apps"}, Spec: vibedv1.VibedAppSpec{Owner: "bob"}},
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
