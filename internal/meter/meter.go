// Package meter is the usage-metering seam.
//
// A Sink records a usage Event on each deploy/delete (and, later, lifecycle
// transitions). The core installs a Prometheus sink (per-tenant event counters);
// an out-of-tree module can register its own — e.g. to emit billable usage
// events — and compose it with Prometheus via Tee. Metering is best-effort and
// off the critical path: Record must not block long and returns no error.
package meter

import (
	"context"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Event is one usage record.
type Event struct {
	// Kind is the event type: "deploy" / "delete" (from the deploy service) or
	// "app.ready" / "app.stopped" (app lifecycle transitions from the controller;
	// a ready→stopped pair bounds a billable runtime interval).
	Kind      string
	Tenant    string
	Owner     string
	App       string
	Namespace string
}

// Sink records usage events.
type Sink interface {
	Record(ctx context.Context, e Event)
}

// Nop discards events.
func Nop() Sink { return nop{} }

type nop struct{}

func (nop) Record(context.Context, Event) {}

// eventsTotal is created (not auto-registered) so it can be registered on the
// right registry per process. The main vibeD server scrapes the DEFAULT
// Prometheus registry (promhttp.Handler); the controller scrapes only
// controller-runtime's Registry. Because lifecycle events (app.ready/stopped)
// are emitted from the CONTROLLER, registering only on the default registry
// left those counters unscraped (#67). We register on the default registry at
// init (for the server) and expose RegisterMetrics for the controller to also
// register on ctrlmetrics.Registry.
var eventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "vibed",
	Name:      "usage_events_total",
	Help:      "Usage events by kind and tenant.",
}, []string{"kind", "tenant"})

var registerCtrlOnce sync.Once

func init() {
	// Default registry — scraped by the main server's promhttp /metrics.
	// MustRegister is safe here: the collector is registered exactly once.
	prometheus.MustRegister(eventsTotal)
}

// RegisterMetrics registers the usage counter with controller-runtime's
// Prometheus registry so the controller's /metrics exposes lifecycle events
// (app.ready/app.stopped) it emits. Idempotent; mirror of
// templatevalidate.RegisterMetrics. Call from the controller's setup.
func RegisterMetrics() {
	registerCtrlOnce.Do(func() { ctrlmetrics.Registry.MustRegister(eventsTotal) })
}

// Prometheus returns a Sink that counts events by kind and tenant (tenant id,
// not name, to bound cardinality; the single-tenant default reports "default").
func Prometheus() Sink { return prom{} }

type prom struct{}

func (prom) Record(_ context.Context, e Event) {
	t := e.Tenant
	if t == "" {
		t = "default"
	}
	eventsTotal.WithLabelValues(e.Kind, t).Inc()
}

// Tee fans an event out to every sink (nil sinks are skipped).
func Tee(sinks ...Sink) Sink { return tee{sinks} }

type tee struct{ sinks []Sink }

func (t tee) Record(ctx context.Context, e Event) {
	for _, s := range t.sinks {
		if s != nil {
			s.Record(ctx, e)
		}
	}
}

// --- registry ---------------------------------------------------------------

// Deps is what a Sink factory receives; Options is a generic settings bag.
type Deps struct {
	Options map[string]string
}

// Factory builds the process Sink. There is at most one.
type Factory func(Deps) (Sink, error)

var (
	mu      sync.Mutex
	factory Factory
)

// Register installs the sink factory. Called from an out-of-tree module's
// init(); it panics if one is already registered.
func Register(f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if factory != nil {
		panic("meter: a sink factory is already registered")
	}
	factory = f
}

// Build returns the registered Sink, or the Prometheus default when none is
// registered.
func Build(deps Deps) (Sink, error) {
	mu.Lock()
	f := factory
	mu.Unlock()
	if f != nil {
		return f(deps)
	}
	return Prometheus(), nil
}
