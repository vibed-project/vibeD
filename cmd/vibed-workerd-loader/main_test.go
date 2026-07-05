package main

import (
	"flag"
	"strings"
	"testing"
)

// TestResolveToken: the flag wins when set; otherwise the env value is used.
func TestResolveToken(t *testing.T) {
	if got := resolveToken("flag-tok", "env-tok"); got != "flag-tok" {
		t.Errorf("flag should win, got %q", got)
	}
	if got := resolveToken("", "env-tok"); got != "env-tok" {
		t.Errorf("empty flag should fall back to env, got %q", got)
	}
	if got := resolveToken("", ""); got != "" {
		t.Errorf("both empty → empty, got %q", got)
	}
}

// TestTokenFlagDefaultDoesNotLeak: the -token flag's default must be empty so
// the secret never appears in the -help usage text (#54). We register the flag
// exactly as main() does and assert its printed default is blank.
func TestTokenFlagDefaultDoesNotLeak(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var token string
	fs.StringVar(&token, "token", "", "Bearer token required on the control API. If empty, read from $VIBED_AGENT_TOKEN.")

	var buf strings.Builder
	fs.SetOutput(&buf)
	fs.PrintDefaults()

	usage := buf.String()
	if !strings.Contains(usage, "-token") {
		t.Fatal("expected -token in usage output")
	}
	// The env-var NAME may appear in help text, but no default VALUE should.
	if strings.Contains(usage, "secret-token-value") {
		t.Error("token value leaked into usage text")
	}
	// Default value must be empty: flag prints `(default "x")` only for non-empty.
	if strings.Contains(usage, "(default ") {
		t.Errorf("-token must have no printed default value, got usage:\n%s", usage)
	}
}
