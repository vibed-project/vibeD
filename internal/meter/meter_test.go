package meter

import (
	"context"
	"testing"

	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

type recorder struct{ events []Event }

func (r *recorder) Record(_ context.Context, e Event) { r.events = append(r.events, e) }

func TestNopAndPrometheusDoNotPanic(t *testing.T) {
	Nop().Record(context.Background(), Event{Kind: "deploy"})
	Prometheus().Record(context.Background(), Event{Kind: "deploy", Tenant: "t1"})
	Prometheus().Record(context.Background(), Event{Kind: "delete"}) // empty tenant -> "default"
}

func TestTeeFansOut(t *testing.T) {
	a, b := &recorder{}, &recorder{}
	Tee(a, nil, b).Record(context.Background(), Event{Kind: "deploy", App: "x"})
	if len(a.events) != 1 || len(b.events) != 1 || a.events[0].App != "x" {
		t.Fatalf("Tee did not fan out: a=%v b=%v", a.events, b.events)
	}
}

// TestRegisterMetricsOnControllerRegistry is the proof for #67: RegisterMetrics
// puts the usage counter on controller-runtime's registry (which the controller
// actually scrapes) so lifecycle events it emits are visible. Idempotent — a
// second call must not panic on duplicate registration.
func TestRegisterMetricsOnControllerRegistry(t *testing.T) {
	RegisterMetrics()
	RegisterMetrics() // idempotent — must not panic

	// Emit an event and confirm it's gatherable from the controller-runtime
	// registry.
	Prometheus().Record(context.Background(), Event{Kind: "app.ready", Tenant: "t1"})

	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	found := false
	for _, f := range families {
		if f.GetName() == "vibed_usage_events_total" {
			found = true
			break
		}
	}
	if !found {
		t.Error("vibed_usage_events_total not registered on controller-runtime registry (would be unscraped in the controller)")
	}
}

// Build returns the Prometheus default until a factory is registered; a second
// Register panics.
func TestBuildAndRegister(t *testing.T) {
	if s, err := Build(Deps{}); err != nil || s == nil {
		t.Fatalf("default Build should be the Prometheus sink: %v, %v", s, err)
	}

	Register(func(Deps) (Sink, error) { return &recorder{}, nil })
	s, err := Build(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*recorder); !ok {
		t.Fatalf("registered sink type = %T, want *recorder", s)
	}

	defer func() {
		if recover() == nil {
			t.Error("a second Register should panic")
		}
	}()
	Register(func(Deps) (Sink, error) { return nil, nil })
}
