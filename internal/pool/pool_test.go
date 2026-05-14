package pool

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/config"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
)

func okProbe(context.Context, string) error   { return nil }
func failProbe(context.Context, string) error { return errors.New("unreachable") }

func newTestPool(t *testing.T, probe probeFunc) *Pool {
	t.Helper()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{sandboxGVR: "SandboxList"},
	)
	cfg := config.FastPathConfig{
		Enabled:      true,
		Namespace:    "vibed-runners",
		ReadyTimeout: 2 * time.Second,
		Runners: map[string]config.RunnerConfig{
			"python": {Image: "vibed-runner-python:dev", PoolSize: 2, ControlPort: 9000, AppPort: 8080},
		},
	}
	p := New(dyn, cfg, "fallback-ns", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if probe != nil {
		p.probe = probe
	}
	return p
}

func sandboxExists(t *testing.T, p *Pool, name string) bool {
	t.Helper()
	_, err := p.dyn.Resource(sandboxGVR).Namespace(p.ns).Get(context.Background(), name, metav1.GetOptions{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get sandbox %s: %v", name, err)
	return false
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestSupportsAndNamespaceOverride(t *testing.T) {
	p := newTestPool(t, okProbe)
	if !p.Supports("python") {
		t.Error("Supports(python) = false, want true")
	}
	if p.Supports("ruby") {
		t.Error("Supports(ruby) = true, want false")
	}
	// cfg.Namespace overrides the fallback.
	if p.ns != "vibed-runners" {
		t.Errorf("namespace = %q, want vibed-runners", p.ns)
	}
}

func TestWarmOneFilesIntoIdle(t *testing.T) {
	p := newTestPool(t, okProbe)
	if err := p.warmOne(context.Background(), "python"); err != nil {
		t.Fatalf("warmOne: %v", err)
	}
	if got := p.IdleCount("python"); got != 1 {
		t.Fatalf("IdleCount = %d, want 1", got)
	}
	r := p.idle["python"][0]
	if !sandboxExists(t, p, r.Name) {
		t.Errorf("warmed runner Sandbox %s not created", r.Name)
	}
}

func TestWarmOneFailureCleansUp(t *testing.T) {
	p := newTestPool(t, failProbe)
	err := p.warmOne(context.Background(), "python")
	if err == nil {
		t.Fatal("warmOne with failing probe should error")
	}
	if got := p.IdleCount("python"); got != 0 {
		t.Errorf("IdleCount = %d, want 0 after failed warmup", got)
	}
	// The Sandbox CR must not be left behind.
	list, _ := p.dyn.Resource(sandboxGVR).Namespace(p.ns).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Errorf("failed warmup left %d Sandbox(es) behind", len(list.Items))
	}
}

func TestClaimWarmPath(t *testing.T) {
	p := newTestPool(t, okProbe)
	if err := p.warmOne(context.Background(), "python"); err != nil {
		t.Fatalf("warmOne: %v", err)
	}
	warmed := p.idle["python"][0].Name

	r, err := p.Claim(context.Background(), "python")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if r.Name != warmed {
		t.Errorf("Claim returned %s, want the warmed runner %s", r.Name, warmed)
	}
	if r.Language != "python" {
		t.Errorf("runner language = %q, want python", r.Language)
	}
}

func TestClaimColdPath(t *testing.T) {
	p := newTestPool(t, okProbe)
	// Empty pool → Claim must create a cold runner rather than failing.
	r, err := p.Claim(context.Background(), "python")
	if err != nil {
		t.Fatalf("cold Claim: %v", err)
	}
	if r == nil || r.Language != "python" {
		t.Fatalf("cold Claim returned %+v", r)
	}
	if !sandboxExists(t, p, r.Name) {
		t.Errorf("cold runner Sandbox %s not created", r.Name)
	}
}

func TestClaimColdPathProbeFailureCleansUp(t *testing.T) {
	p := newTestPool(t, failProbe)
	_, err := p.Claim(context.Background(), "python")
	if err == nil {
		t.Fatal("cold Claim with failing probe should error")
	}
	list, _ := p.dyn.Resource(sandboxGVR).Namespace(p.ns).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Errorf("failed cold Claim left %d Sandbox(es) behind", len(list.Items))
	}
}

func TestClaimUnsupportedLanguage(t *testing.T) {
	p := newTestPool(t, okProbe)
	_, err := p.Claim(context.Background(), "rust")
	if !errors.Is(err, ErrLanguageUnsupported) {
		t.Fatalf("Claim(rust) error = %v, want ErrLanguageUnsupported", err)
	}
}

func TestReleaseRecyclesRunner(t *testing.T) {
	p := newTestPool(t, okProbe)
	if err := p.warmOne(context.Background(), "python"); err != nil {
		t.Fatalf("warmOne: %v", err)
	}
	r := p.idle["python"][0]

	p.Release(context.Background(), r)

	// The Sandbox must be deleted (recycled, never reused).
	eventually(t, func() bool { return !sandboxExists(t, p, r.Name) })
	p.mu.Lock()
	_, stillTracked := p.tracked[r.Name]
	p.mu.Unlock()
	if stillTracked {
		t.Error("released runner still tracked")
	}
}

func TestReplenishFillsToPoolSize(t *testing.T) {
	p := newTestPool(t, okProbe)
	p.replenish()
	eventually(t, func() bool { return p.IdleCount("python") == 2 })

	// A second replenish is a no-op once the pool is full.
	p.replenish()
	time.Sleep(50 * time.Millisecond)
	if got := p.IdleCount("python"); got != 2 {
		t.Errorf("IdleCount after second replenish = %d, want 2", got)
	}
}

func TestDrainDeletesAllRunners(t *testing.T) {
	p := newTestPool(t, okProbe)
	// warmOne does not trigger async replenish, so the count is deterministic.
	for i := 0; i < 3; i++ {
		if err := p.warmOne(context.Background(), "python"); err != nil {
			t.Fatalf("warmOne: %v", err)
		}
	}
	p.drain()

	list, _ := p.dyn.Resource(sandboxGVR).Namespace(p.ns).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Errorf("drain left %d Sandbox(es) behind", len(list.Items))
	}
	if got := p.IdleCount("python"); got != 0 {
		t.Errorf("IdleCount after drain = %d, want 0", got)
	}
}

func TestSweepEvictsStaleAndUnhealthy(t *testing.T) {
	p := newTestPool(t, okProbe)
	p.cfg.MaxIdleAge = time.Hour
	if err := p.warmOne(context.Background(), "python"); err != nil {
		t.Fatalf("warmOne: %v", err)
	}
	stale := p.idle["python"][0]
	// Force the runner past its max idle age.
	stale.CreatedAt = time.Now().Add(-2 * time.Hour)

	p.sweep(context.Background())

	eventually(t, func() bool { return !sandboxExists(t, p, stale.Name) })
}
