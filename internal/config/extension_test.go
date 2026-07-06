package config

import "testing"

// The config validator must accept store backends / auth modes that are
// registered out of tree (the enterprise "postgres" store and "saml" mode) once
// the server injects the registry listers, while staying strict by default.
// This is the activation gate for the enterprise binary: before it, a licensed
// build fataled on store.backend=postgres / auth.mode=saml at config load,
// before the plugin registry was ever consulted.

func TestValidate_StoreBackend_RegistryAware(t *testing.T) {
	t.Cleanup(func() { SetStoreBackendLister(nil); SetAuthModeLister(nil) })

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("default config should load: %v", err)
	}

	// Strict by default: an unregistered backend is rejected.
	cfg.Store.Backend = "postgres"
	if err := validate(cfg); err == nil {
		t.Fatal("store.backend=postgres must be rejected when no lister is injected")
	}

	// Accepted once the registry reports it (as LoadConfig wires up).
	SetStoreBackendLister(func() []string { return []string{"memory", "configmap", "sqlite", "postgres"} })
	if err := validate(cfg); err != nil {
		t.Fatalf("store.backend=postgres must be accepted once registered: %v", err)
	}

	// A still-unknown backend is rejected even with a lister present.
	cfg.Store.Backend = "cassandra"
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
	cfg.Auth.Mode = "saml"

	// Strict by default: saml is rejected before the registry is consulted.
	if err := validate(cfg); err == nil {
		t.Fatal("auth.mode=saml must be rejected when no lister is injected")
	}

	// Accepted once registered. saml provisions its own users and needs no API
	// keys, unlike the apikey default — validation must pass with none set.
	SetAuthModeLister(func() []string { return []string{"apikey", "oauth", "oidc", "saml"} })
	if err := validate(cfg); err != nil {
		t.Fatalf("auth.mode=saml must be accepted once registered: %v", err)
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
