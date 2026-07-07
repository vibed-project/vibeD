package config

import "testing"

// The config validator must accept store backends / auth modes registered out
// of tree via pkg/plugin once the server injects the registry listers, while
// staying strict by default. Before this, a build that registered an
// out-of-tree backend/mode fataled at config load because the validator ran
// against the built-in allow-list before the registry was consulted.

func TestValidate_StoreBackend_RegistryAware(t *testing.T) {
	t.Cleanup(func() { SetStoreBackendLister(nil); SetAuthModeLister(nil) })

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("default config should load: %v", err)
	}

	// Strict by default: an unregistered backend is rejected.
	cfg.Store.Backend = "customstore"
	if err := validate(cfg); err == nil {
		t.Fatal("an out-of-tree store.backend must be rejected when no lister is injected")
	}

	// Accepted once the registry reports it (as LoadConfig wires up).
	SetStoreBackendLister(func() []string { return []string{"memory", "configmap", "sqlite", "customstore"} })
	if err := validate(cfg); err != nil {
		t.Fatalf("a registered store.backend must be accepted: %v", err)
	}

	// A still-unknown backend is rejected even with a lister present.
	cfg.Store.Backend = "nosuchstore"
	if err := validate(cfg); err == nil {
		t.Fatal("an unregistered backend must still be rejected")
	}
}

func TestValidate_AuthMode_RegistryAware(t *testing.T) {
	t.Cleanup(func() { SetStoreBackendLister(nil); SetAuthModeLister(nil) })

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("default config should load: %v", err)
	}
	cfg.Auth.Enabled = true
	cfg.Auth.Mode = "customauth"

	// Strict by default: rejected before the registry is consulted.
	if err := validate(cfg); err == nil {
		t.Fatal("an out-of-tree auth.mode must be rejected when no lister is injected")
	}

	// Accepted once registered. A non-built-in mode (not apikey/oauth/"") carries
	// no API keys, so validation must pass with none configured.
	SetAuthModeLister(func() []string { return []string{"apikey", "oauth", "oidc", "customauth"} })
	if err := validate(cfg); err != nil {
		t.Fatalf("a registered auth.mode must be accepted: %v", err)
	}
}

func TestValidate_BuiltinsValidWithoutListers(t *testing.T) {
	// Regression guard: the built-in backends/modes must stay valid when no
	// lister is injected (bare config load, tools that never boot a server).
	SetStoreBackendLister(nil)
	SetAuthModeLister(nil)

	for _, backend := range []string{"memory", "configmap", "sqlite"} {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("default config should load: %v", err)
		}
		if backend == "sqlite" {
			cfg.Store.SQLite.Path = "/tmp/vibed.db"
		}
		cfg.Store.Backend = backend
		if err := validate(cfg); err != nil {
			t.Fatalf("built-in store.backend=%s must validate without a lister: %v", backend, err)
		}
	}
}
