package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSecret_Builtins(t *testing.T) {
	// Literal (no scheme) — returned as-is.
	if got, err := ResolveSecret("plain-value"); err != nil || got != "plain-value" {
		t.Fatalf("literal: got %q, %v", got, err)
	}
	// Unregistered scheme (e.g. a URL) — returned as-is, not an error.
	if got, err := ResolveSecret("https://example.com/x"); err != nil || got != "https://example.com/x" {
		t.Fatalf("unregistered scheme: got %q, %v", got, err)
	}

	// env: set and unset.
	t.Setenv("VIBED_TEST_SECRET", "s3cr3t")
	if got, err := ResolveSecret("env:VIBED_TEST_SECRET"); err != nil || got != "s3cr3t" {
		t.Fatalf("env set: got %q, %v", got, err)
	}
	if _, err := ResolveSecret("env:VIBED_TEST_MISSING_VAR"); err == nil {
		t.Error("env unset should error")
	}

	// file: reads + trims.
	f := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(f, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveSecret("file:" + f); err != nil || got != "file-secret" {
		t.Fatalf("file: got %q, %v", got, err)
	}
	if _, err := ResolveSecret("file:/no/such/file"); err == nil {
		t.Error("missing file should error")
	}
}

// An out-of-tree module registers a scheme exactly this way; the ref is the part
// after the first ':'.
func TestRegisterScheme(t *testing.T) {
	RegisterScheme("test-vault", func(_ context.Context, ref string) (string, error) {
		return "resolved:" + ref, nil
	})
	if got, err := ResolveSecret("test-vault:secret/data/db#pw"); err != nil || got != "resolved:secret/data/db#pw" {
		t.Fatalf("custom scheme: got %q, %v", got, err)
	}

	defer func() {
		if recover() == nil {
			t.Error("duplicate RegisterScheme should panic")
		}
	}()
	RegisterScheme("test-vault", func(context.Context, string) (string, error) { return "", nil })
}
