package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type gateFunc func(context.Context, Input) error

func (f gateFunc) Evaluate(ctx context.Context, in Input) error { return f(ctx, in) }

func TestDeniedError(t *testing.T) {
	var err error = &DeniedError{Reason: "disallowed egress"}
	var pe interface{ PolicyDenied() bool }
	if !errors.As(err, &pe) || !pe.PolicyDenied() {
		t.Fatal("DeniedError must satisfy the PolicyDenied() detector")
	}
	if !strings.Contains(err.Error(), "disallowed egress") {
		t.Errorf("error = %q", err.Error())
	}
}

// Build returns nil (no policy) until a factory is registered; a second Register
// panics.
func TestBuildAndRegister(t *testing.T) {
	g, err := Build(Deps{})
	if err != nil || g != nil {
		t.Fatalf("default Build should be a nil gate, got %v, %v", g, err)
	}

	Register(func(Deps) (Gate, error) {
		return gateFunc(func(context.Context, Input) error { return nil }), nil
	})
	if g, err := Build(Deps{}); err != nil || g == nil {
		t.Fatalf("registered gate: %v, %v", g, err)
	}

	defer func() {
		if recover() == nil {
			t.Error("a second Register should panic")
		}
	}()
	Register(func(Deps) (Gate, error) { return nil, nil })
}
