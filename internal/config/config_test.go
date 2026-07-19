package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIdentityCacheTTL_Default: the identity cache defaults to 30s — the
// suspension/role-change propagation bound documented on the field.
func TestIdentityCacheTTL_Default(t *testing.T) {
	if got := Default().Auth.IdentityCacheTTL; got != 30*time.Second {
		t.Errorf("Default().Auth.IdentityCacheTTL = %v, want 30s", got)
	}
}

// TestIdentityCacheTTL_Load: the knob parses duration strings from yaml, an
// explicit zero disables caching, and an absent field keeps the default.
func TestIdentityCacheTTL_Load(t *testing.T) {
	load := func(t *testing.T, yaml string) *Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "vibed.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return cfg
	}

	cfg := load(t, "auth:\n  identityCacheTTL: 5s\n")
	if cfg.Auth.IdentityCacheTTL != 5*time.Second {
		t.Errorf("explicit 5s: got %v", cfg.Auth.IdentityCacheTTL)
	}

	cfg = load(t, "auth:\n  identityCacheTTL: 0s\n")
	if cfg.Auth.IdentityCacheTTL != 0 {
		t.Errorf("explicit 0s (caching disabled): got %v", cfg.Auth.IdentityCacheTTL)
	}

	cfg = load(t, "auth: {}\n")
	if cfg.Auth.IdentityCacheTTL != 30*time.Second {
		t.Errorf("absent field should keep the 30s default: got %v", cfg.Auth.IdentityCacheTTL)
	}
}
