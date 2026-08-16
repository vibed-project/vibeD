package deploy

import (
	"strings"
	"testing"
)

// The blob endpoint authenticates callers but cannot authorize them per app
// (the agent fetches with one shared token). A key derived only from name and
// version was therefore guessable by any authenticated client, who could read
// another user's source. The key must be unpredictable.
func TestNewVersionKey_IsUnguessable(t *testing.T) {
	const name, version = "shop", 1

	k1, err := newVersionKey(name, version)
	if err != nil {
		t.Fatalf("newVersionKey: %v", err)
	}
	k2, err := newVersionKey(name, version)
	if err != nil {
		t.Fatalf("newVersionKey: %v", err)
	}

	// Same inputs must not produce the same key.
	if k1 == k2 {
		t.Fatalf("key is deterministic (%q) — an attacker can derive it from the app name", k1)
	}
	// The old, guessable form must no longer be the whole key.
	if k1 == "shop.v1" {
		t.Fatal("key is still the guessable name.vN form")
	}
	// It should still start with the readable prefix (operator legibility)...
	if !strings.HasPrefix(k1, "shop.v1.") {
		t.Errorf("key %q lost its shop.v1 prefix", k1)
	}
	// ...followed by 128 bits of hex entropy.
	suffix := strings.TrimPrefix(k1, "shop.v1.")
	if len(suffix) != 32 {
		t.Errorf("random suffix %q is %d hex chars, want 32 (128 bits)", suffix, len(suffix))
	}
}

// Distinct apps and versions must never collide onto one blob.
func TestNewVersionKey_NoCollisions(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k, err := newVersionKey("shop", 1)
		if err != nil {
			t.Fatalf("newVersionKey: %v", err)
		}
		if seen[k] {
			t.Fatalf("duplicate key %q after %d draws", k, i)
		}
		seen[k] = true
	}
}
