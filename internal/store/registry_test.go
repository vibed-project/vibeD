package store

import (
	"errors"
	"testing"
)

// The three core backends must self-register via init().
func TestRegistry_CoreBackendsRegistered(t *testing.T) {
	got := Backends()
	for _, want := range []string{"memory", "sqlite", "configmap"} {
		if !contains(got, want) {
			t.Errorf("backend %q not registered; Backends() = %v", want, got)
		}
	}
}

func TestNew_Memory(t *testing.T) {
	st, err := New(Deps{Backend: "memory"})
	if err != nil {
		t.Fatalf("New(memory) error: %v", err)
	}
	if st == nil {
		t.Fatal("New(memory) returned nil store")
	}
	// The memory backend also implements AuditStore but not UserStore — the
	// feature-detection main.go relies on must hold.
	if _, ok := st.(UserStore); ok {
		t.Error("memory backend unexpectedly implements UserStore")
	}
	if _, ok := st.(AuditStore); !ok {
		t.Error("memory backend should implement AuditStore")
	}
}

func TestNew_Sqlite(t *testing.T) {
	st, err := New(Deps{Backend: "sqlite", SQLitePath: t.TempDir() + "/vibed.db"})
	if err != nil {
		t.Fatalf("New(sqlite) error: %v", err)
	}
	// SQLite is the one core backend that carries user identity + share links.
	if _, ok := st.(UserStore); !ok {
		t.Error("sqlite backend should implement UserStore")
	}
	if c, ok := st.(interface{ Close() error }); ok {
		_ = c.Close()
	} else {
		t.Error("sqlite backend should implement io.Closer for shutdown")
	}
}

func TestNew_UnknownBackend(t *testing.T) {
	_, err := New(Deps{Backend: "nope"})
	if !errors.Is(err, ErrUnknownBackend) {
		t.Fatalf("New(nope) error = %v, want ErrUnknownBackend", err)
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register of a duplicate backend should panic")
		}
	}()
	Register("memory", func(Deps) (ArtifactStore, error) { return NewMemoryStore(), nil })
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register with a nil factory should panic")
		}
	}()
	Register("panic-nil", nil)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
