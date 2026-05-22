package workerd

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newLoader(t *testing.T) (*Loader, *int) {
	t.Helper()
	var reloads int
	var mu sync.Mutex
	l, err := New(Options{
		BaseDir:  t.TempDir(),
		PortBase: 9100,
		Reload: func() error {
			mu.Lock()
			defer mu.Unlock()
			reloads++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l, &reloads
}

func TestPutWritesScriptAssignsPortAndReloads(t *testing.T) {
	l, reloads := newLoader(t)

	port, err := l.Put("abc123", "export default { fetch() { return new Response('hi') } }")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if port != 9100 {
		t.Errorf("first app port = %d, want 9100", port)
	}
	if *reloads != 1 {
		t.Errorf("reloads = %d, want 1", *reloads)
	}

	// Script landed on disk.
	got, err := os.ReadFile(filepath.Join(l.baseDir, "scripts", "abc123", "worker.js"))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if !strings.Contains(string(got), "Response('hi')") {
		t.Errorf("script content not written: %q", got)
	}

	// config.capnp references the worker + socket on the assigned port.
	cfg, err := os.ReadFile(l.ConfigPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfgStr := string(cfg)
	for _, want := range []string{"const worker_abc123", `"app_abc123"`, "*:9100"} {
		if !strings.Contains(cfgStr, want) {
			t.Errorf("config missing %q:\n%s", want, cfgStr)
		}
	}
}

func TestPutIsIdempotentOnPort(t *testing.T) {
	l, _ := newLoader(t)
	p1, _ := l.Put("app", "v1")
	p2, _ := l.Put("app", "v2-updated")
	if p1 != p2 {
		t.Errorf("re-Put changed port: %d -> %d", p1, p2)
	}
	got, _ := os.ReadFile(filepath.Join(l.baseDir, "scripts", "app", "worker.js"))
	if string(got) != "v2-updated" {
		t.Errorf("re-Put didn't overwrite script: %q", got)
	}
}

func TestMultipleAppsGetDistinctPorts(t *testing.T) {
	l, _ := newLoader(t)
	pa, _ := l.Put("a", "x")
	pb, _ := l.Put("b", "y")
	pc, _ := l.Put("c", "z")
	if pa == pb || pb == pc || pa == pc {
		t.Errorf("ports collided: %d %d %d", pa, pb, pc)
	}

	// config lists all three services + sockets.
	cfg, _ := os.ReadFile(l.ConfigPath())
	for _, want := range []string{"app_a", "app_b", "app_c"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config missing service %q", want)
		}
	}
}

func TestRemoveFreesPortForReuse(t *testing.T) {
	l, _ := newLoader(t)
	pa, _ := l.Put("a", "x") // 9100
	_, _ = l.Put("b", "y")   // 9101
	if err := l.Remove("a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if l.Port("a") != 0 {
		t.Errorf("removed app still has a port")
	}
	// New app reuses the lowest free slot (a's old port).
	pc, _ := l.Put("c", "z")
	if pc != pa {
		t.Errorf("port not reused: got %d, want %d", pc, pa)
	}

	// a's script dir is gone; a no longer in config.
	if _, err := os.Stat(filepath.Join(l.baseDir, "scripts", "a")); !os.IsNotExist(err) {
		t.Errorf("removed app's script dir still exists")
	}
	cfg, _ := os.ReadFile(l.ConfigPath())
	if strings.Contains(string(cfg), "worker_a ") || strings.Contains(string(cfg), `"app_a"`) {
		t.Errorf("config still references removed app:\n%s", cfg)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	l, _ := newLoader(t)
	// Insert in non-sorted order; rendering must sort for stable output.
	_, _ = l.Put("zebra", "z")
	_, _ = l.Put("alpha", "a")
	cfg1, _ := os.ReadFile(l.ConfigPath())
	// Re-render by re-putting the same content; output must be byte-identical.
	_, _ = l.Put("alpha", "a")
	cfg2, _ := os.ReadFile(l.ConfigPath())
	if string(cfg1) != string(cfg2) {
		t.Errorf("render not deterministic:\n--- 1 ---\n%s\n--- 2 ---\n%s", cfg1, cfg2)
	}
	// alpha must appear before zebra (sorted).
	if strings.Index(string(cfg1), "worker_alpha") > strings.Index(string(cfg1), "worker_zebra") {
		t.Errorf("services not sorted")
	}
}

func TestPutRejectsBadID(t *testing.T) {
	l, _ := newLoader(t)
	for _, bad := range []string{"", "a/b", "..", "../escape"} {
		if _, err := l.Put(bad, "x"); err == nil {
			t.Errorf("Put(%q) should error", bad)
		}
	}
}

func TestReloadFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	l, err := New(Options{BaseDir: dir, Reload: func() error { return os.ErrPermission }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Put("app", "x"); err == nil {
		t.Error("Put should surface a reload failure")
	}
}
