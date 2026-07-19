package authz

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/labels"
)

// plainFake implements only Authorizer — the pre-existing implementor shape
// that must keep working untouched.
type plainFake struct{}

func (plainFake) Authorize(context.Context, Request) error { return nil }

// extendedFake layers both optional list interfaces on the same Authorizer.
type extendedFake struct{ plainFake }

func (extendedFake) ScopeList(context.Context, Request) (labels.Selector, error) {
	return labels.Everything(), nil
}

func (extendedFake) AuthorizeBatch(_ context.Context, reqs []Request) []error {
	return make([]error, len(reqs))
}

// Compile-time: the optional interfaces are additive — plainFake satisfies
// Authorizer without them, extendedFake satisfies all three.
var (
	_ Authorizer      = plainFake{}
	_ Authorizer      = extendedFake{}
	_ ListScoper      = extendedFake{}
	_ BatchAuthorizer = extendedFake{}
)

// TestOptionalInterfacesSurviveRegistry: callers discover ListScoper and
// BatchAuthorizer by type assertion on the Authorizer that Build returns, so
// the registry must hand back the concrete type unlaundered — and a plain
// Authorizer must not accidentally satisfy the extensions.
func TestOptionalInterfacesSurviveRegistry(t *testing.T) {
	Register(func(Deps) (Authorizer, error) { return extendedFake{}, nil })
	a, err := Build(Deps{})
	if err != nil || a == nil {
		t.Fatalf("Build = %v, %v; want the registered authorizer", a, err)
	}
	if _, ok := a.(ListScoper); !ok {
		t.Fatalf("built Authorizer lost ListScoper")
	}
	if _, ok := a.(BatchAuthorizer); !ok {
		t.Fatalf("built Authorizer lost BatchAuthorizer")
	}
	var plain Authorizer = plainFake{}
	if _, ok := plain.(ListScoper); ok {
		t.Fatalf("plain Authorizer unexpectedly implements ListScoper")
	}
	if _, ok := plain.(BatchAuthorizer); ok {
		t.Fatalf("plain Authorizer unexpectedly implements BatchAuthorizer")
	}
}
