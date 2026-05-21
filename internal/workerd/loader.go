// Package workerd implements the fast-lane loader for Cloudflare workerd.
//
// One workerd process per StatefulSet replica holds many isolates; the loader
// (a sidecar to that process) owns the set of deployed scripts. On each deploy
// it writes the user's worker script to disk, re-renders workerd's config
// (config.capnp), and signals workerd to reload — no microVM, no container
// build. This is the "fast" lane from refactor.md §5.6, used for classifier
// rule 3 (worker.js / worker.ts / wrangler.toml).
//
// IMPORTANT: the config.capnp schema and the multi-tenant routing model below
// are written to the documented workerd schema but have NOT been validated
// against a live workerd binary in this environment. The Go-side contract
// (script management, port assignment, deterministic rendering, reload
// triggering) is fully tested; treat the exact capnp text as needing a
// real-workerd smoke test before production.
package workerd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// scriptFile is the fixed name each app's worker module is written under,
// inside its own per-app directory. workerd's esModule embed references it.
const scriptFile = "worker.js"

// Loader owns the on-disk scripts and the rendered workerd config for one
// workerd replica. Safe for concurrent use.
type Loader struct {
	// baseDir holds <baseDir>/scripts/<app>/worker.js and <baseDir>/config.capnp.
	baseDir string
	// compatDate is workerd's compatibilityDate applied to every worker.
	compatDate string

	// portBase is the first port assigned to an app socket; each app gets
	// portBase + its slot. Caddy routes <label>.domain -> replica:port.
	portBase int

	// reload is invoked after every config change so workerd picks it up.
	// Injectable so tests don't need a real workerd process.
	reload func() error

	mu    sync.Mutex
	apps  map[string]int // app id -> assigned port
	ports map[int]string // assigned port -> app id (for slot reuse)
}

// Options configures a Loader. ScriptsBaseDir is required.
type Options struct {
	BaseDir    string
	CompatDate string
	PortBase   int
	// Reload is called after each config render. Nil = no-op (tests / dry runs).
	Reload func() error
}

// New constructs a Loader and ensures its scripts dir exists.
func New(opts Options) (*Loader, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("workerd: BaseDir is required")
	}
	if opts.CompatDate == "" {
		opts.CompatDate = "2024-01-01"
	}
	if opts.PortBase == 0 {
		opts.PortBase = 9100
	}
	if err := os.MkdirAll(filepath.Join(opts.BaseDir, "scripts"), 0o755); err != nil {
		return nil, fmt.Errorf("workerd: create scripts dir: %w", err)
	}
	reload := opts.Reload
	if reload == nil {
		reload = func() error { return nil }
	}
	l := &Loader{
		baseDir:    opts.BaseDir,
		compatDate: opts.CompatDate,
		portBase:   opts.PortBase,
		reload:     reload,
		apps:       map[string]int{},
		ports:      map[int]string{},
	}
	// Render an initial (empty) config so `workerd serve` has a valid file to
	// start from before any app is deployed. No reload — workerd isn't up yet.
	if err := l.renderLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// Put writes (or replaces) the worker script for app id, re-renders the
// config, reloads workerd, and returns the port the app's socket listens on.
// Idempotent: re-Putting the same id keeps its port.
func (l *Loader) Put(id, script string) (int, error) {
	if id == "" {
		return 0, fmt.Errorf("workerd: app id is empty")
	}
	if !validID(id) {
		return 0, fmt.Errorf("workerd: invalid app id %q", id)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	dir := filepath.Join(l.baseDir, "scripts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("workerd: mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, scriptFile), []byte(script), 0o644); err != nil {
		return 0, fmt.Errorf("workerd: write script: %w", err)
	}

	port, ok := l.apps[id]
	if !ok {
		port = l.assignPortLocked(id)
	}

	if err := l.renderLocked(); err != nil {
		return 0, err
	}
	if err := l.reload(); err != nil {
		return 0, fmt.Errorf("workerd: reload: %w", err)
	}
	return port, nil
}

// Remove deletes the app's script and socket, re-renders, and reloads. A
// missing app is not an error.
func (l *Loader) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if port, ok := l.apps[id]; ok {
		delete(l.apps, id)
		delete(l.ports, port)
	}
	if err := os.RemoveAll(filepath.Join(l.baseDir, "scripts", id)); err != nil {
		return fmt.Errorf("workerd: remove script: %w", err)
	}
	if err := l.renderLocked(); err != nil {
		return err
	}
	return l.reload()
}

// Port returns the assigned port for id, or 0 if not deployed.
func (l *Loader) Port(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.apps[id]
}

// ConfigPath is where the rendered config.capnp lives (workerd's -c arg).
func (l *Loader) ConfigPath() string {
	return filepath.Join(l.baseDir, "config.capnp")
}

// assignPortLocked picks the lowest free slot so removed apps' ports get
// reused — keeps the port range compact. Caller holds l.mu.
func (l *Loader) assignPortLocked(id string) int {
	for slot := 0; ; slot++ {
		port := l.portBase + slot
		if _, taken := l.ports[port]; !taken {
			l.apps[id] = port
			l.ports[port] = id
			return port
		}
	}
}

// renderLocked writes config.capnp from the current app set. Caller holds l.mu.
func (l *Loader) renderLocked() error {
	ids := make([]string, 0, len(l.apps))
	for id := range l.apps {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic output

	cfg := renderConfig(ids, l.apps, l.compatDate)
	tmp := l.ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(cfg), 0o644); err != nil {
		return fmt.Errorf("workerd: write config: %w", err)
	}
	if err := os.Rename(tmp, l.ConfigPath()); err != nil {
		return fmt.Errorf("workerd: swap config: %w", err)
	}
	return nil
}

// renderConfig produces the config.capnp text for the given apps. Each app is
// a Worker service with an esModule loaded from its script file, bound to its
// own HTTP socket on the assigned port. Routing by hostname is handled
// upstream by vibed-router/Caddy, so each app just needs a reachable port.
func renderConfig(ids []string, ports map[string]int, compatDate string) string {
	var b strings.Builder
	b.WriteString(`using Workerd = import "/workerd/workerd.capnp";` + "\n\n")

	b.WriteString("const config :Workerd.Config = (\n")
	b.WriteString("  services = [\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "    (name = %q, worker = .worker_%s),\n", svcName(id), goIdent(id))
	}
	b.WriteString("  ],\n")
	b.WriteString("  sockets = [\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "    (name = %q, address = \"*:%d\", http = (), service = %q),\n",
			"sock_"+goIdent(id), ports[id], svcName(id))
	}
	b.WriteString("  ],\n")
	b.WriteString(");\n\n")

	for _, id := range ids {
		fmt.Fprintf(&b, "const worker_%s :Workerd.Worker = (\n", goIdent(id))
		fmt.Fprintf(&b, "  modules = [ (name = %q, esModule = embed %q) ],\n",
			scriptFile, filepath.Join("scripts", id, scriptFile))
		fmt.Fprintf(&b, "  compatibilityDate = %q,\n", compatDate)
		b.WriteString(");\n\n")
	}
	return b.String()
}

func svcName(id string) string { return "app_" + goIdent(id) }

// goIdent makes an app id safe as a capnp constant suffix (alnum + _).
func goIdent(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func validID(id string) bool {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
		return false
	}
	return true
}
