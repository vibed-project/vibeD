package tenant

import (
	"context"
	"testing"
)

func TestSingleResolver(t *testing.T) {
	got, err := Single(Tenant{ID: "t1", Namespace: "ns1"}).Resolve(context.Background())
	if err != nil || got.ID != "t1" || got.Namespace != "ns1" {
		t.Fatalf("Single.Resolve = %+v, %v", got, err)
	}
}

// Build returns the single-tenant default until a factory is registered, after
// which the registered resolver takes over; a second Register panics.
func TestBuildAndRegister(t *testing.T) {
	r, err := Build(Deps{Default: Tenant{Namespace: "apps"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Resolve(context.Background()); got.Namespace != "apps" || got.ID != "" {
		t.Fatalf("default resolver = %+v, want {Namespace: apps}", got)
	}

	Register(func(Deps) (Resolver, error) {
		return Single(Tenant{ID: "custom", Namespace: "custom-ns"}), nil
	})
	r, err = Build(Deps{Default: Tenant{Namespace: "apps"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Resolve(context.Background()); got.ID != "custom" || got.Namespace != "custom-ns" {
		t.Fatalf("registered resolver = %+v", got)
	}

	defer func() {
		if recover() == nil {
			t.Error("a second Register should panic")
		}
	}()
	Register(func(Deps) (Resolver, error) { return nil, nil })
}
