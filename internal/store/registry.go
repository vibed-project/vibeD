package store

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"k8s.io/client-go/kubernetes"
)

// Deps carries everything a backend Factory might need to build a store. Core
// backends read only the fields they care about (sqlite → SQLitePath; configmap
// → K8sClient + ConfigMapName/Namespace; memory → nothing). Options is a generic
// escape hatch so out-of-tree backends (e.g. an enterprise Postgres store) can
// receive their own configuration (a DSN, TLS settings, …) without the core
// StoreConfig having to grow a field for every backend.
type Deps struct {
	Backend            string
	SQLitePath         string
	ConfigMapName      string
	ConfigMapNamespace string
	K8sClient          kubernetes.Interface
	Options            map[string]string
	Logger             *slog.Logger
}

// Factory builds a primary ArtifactStore from Deps. The returned store MAY also
// implement UserStore, AuditStore, ShareLinkStore, and io.Closer; callers
// feature-detect those via type assertion. Registered from each backend's
// init(); enterprise editions register additional backends the same way.
type Factory func(Deps) (ArtifactStore, error)

var (
	registryMu sync.RWMutex
	factories  = map[string]Factory{}
)

// ErrUnknownBackend is returned by New when no factory is registered for the
// configured backend name.
var ErrUnknownBackend = errors.New("unknown store backend")

// Register adds a backend factory under name. It is called from backend init()
// functions, so registration happens before main() runs. It panics on a nil
// factory or a duplicate name — both are programmer errors, not runtime states.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if f == nil {
		panic("store: Register factory is nil for backend " + name)
	}
	if _, dup := factories[name]; dup {
		panic("store: Register called twice for backend " + name)
	}
	factories[name] = f
}

// New builds the store for deps.Backend. It returns ErrUnknownBackend (wrapped,
// with the list of registered backends) when the backend is not registered.
func New(deps Deps) (ArtifactStore, error) {
	registryMu.RLock()
	f, ok := factories[deps.Backend]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q (available: %s)", ErrUnknownBackend, deps.Backend, strings.Join(Backends(), ", "))
	}
	return f(deps)
}

// Backends returns the registered backend names, sorted — for validation and
// error messages.
func Backends() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
