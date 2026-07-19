package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
)

// TestOAuthPassthroughTrustsForwardedUserOnlyFromTrustedProxy locks in the fix
// for the X-Forwarded-User spoofing issue: the identity header is honored only
// when the request originates from a configured trusted-proxy CIDR.
func TestOAuthPassthroughTrustsForwardedUserOnlyFromTrustedProxy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const secret = "proxy-secret"
	keys := []config.APIKeyConf{{Key: secret, Name: "proxy"}}
	trusted := parseTrustedProxies([]string{"10.0.0.0/8"}, logger)
	verifier := oauthPassthroughVerifier(resolveAPIKeys(keys, logger), trusted, logger)

	userIDFrom := func(remote, fwd string) string {
		r := httptest.NewRequest("GET", "/mcp", nil)
		r.RemoteAddr = remote
		if fwd != "" {
			r.Header.Set("X-Forwarded-User", fwd)
		}
		info, err := verifier(context.Background(), secret, r)
		if err != nil {
			t.Fatalf("verifier(%s,%s): unexpected error %v", remote, fwd, err)
		}
		return info.UserID
	}

	// From a trusted proxy → the forwarded identity is honored.
	if got := userIDFrom("10.1.2.3:5555", "alice"); got != "alice" {
		t.Errorf("trusted proxy: UserID = %q, want alice", got)
	}
	// From an untrusted source → the header is ignored (generic identity).
	if got := userIDFrom("203.0.113.9:4444", "admin"); got != "oauth-user" {
		t.Errorf("untrusted source: UserID = %q, want oauth-user (header must not be trusted)", got)
	}
	// No header → generic identity.
	if got := userIDFrom("10.1.2.3:5555", ""); got != "oauth-user" {
		t.Errorf("no header: UserID = %q, want oauth-user", got)
	}

	// With no trusted proxies configured, the header is never trusted.
	verifierNoTrust := oauthPassthroughVerifier(resolveAPIKeys(keys, logger), nil, logger)
	r := httptest.NewRequest("GET", "/mcp", nil)
	r.RemoteAddr = "10.1.2.3:5555"
	r.Header.Set("X-Forwarded-User", "admin")
	info, err := verifierNoTrust(context.Background(), secret, r)
	if err != nil {
		t.Fatalf("no-trust verifier: %v", err)
	}
	if info.UserID != "oauth-user" {
		t.Errorf("no trusted proxies: UserID = %q, want oauth-user", info.UserID)
	}

	// A wrong proxy secret is still rejected.
	r2 := httptest.NewRequest("GET", "/mcp", nil)
	r2.RemoteAddr = "10.1.2.3:5555"
	if _, err := verifier(context.Background(), "wrong", r2); err == nil {
		t.Error("expected error for invalid proxy secret")
	}
}
