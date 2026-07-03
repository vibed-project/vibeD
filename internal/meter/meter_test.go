package meter

import (
	"context"
	"testing"
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
