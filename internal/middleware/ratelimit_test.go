package middleware

import (
	"net/http/httptest"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
)

// TestRateLimitKey is the proof for #56: the key comes from the authenticated
// identity in context (the trusted-proxy-validated user in oauth mode) or the
// socket peer address — NEVER from the X-Forwarded-User header.
func TestRateLimitKey(t *testing.T) {
	// Authenticated request → keyed by the context user ID.
	req := httptest.NewRequest("GET", "/api/artifacts", nil)
	req.RemoteAddr = "203.0.113.5:4444"
	req = req.WithContext(vibedauth.WithUserID(req.Context(), "apikey-alice"))
	if key, typ := rateLimitKey(req); key != "apikey-alice" || typ != "apikey" {
		t.Errorf("authenticated key = (%q,%q), want (apikey-alice, apikey)", key, typ)
	}

	// Unauthenticated request → keyed by RemoteAddr host, header IGNORED.
	req2 := httptest.NewRequest("GET", "/api/artifacts", nil)
	req2.RemoteAddr = "198.51.100.9:5555"
	req2.Header.Set("X-Forwarded-User", "admin") // attacker-controlled, must be ignored
	if key, typ := rateLimitKey(req2); key != "198.51.100.9" || typ != "ip" {
		t.Errorf("unauthenticated key = (%q,%q), want (198.51.100.9, ip)", key, typ)
	}

	// A spoofed forwarded header does NOT change the key vs. no header.
	req3 := httptest.NewRequest("GET", "/api/artifacts", nil)
	req3.RemoteAddr = "198.51.100.9:6666"
	keyNoHeader, _ := rateLimitKey(req3)
	req3.Header.Set("X-Forwarded-User", "someone-else")
	keyWithHeader, _ := rateLimitKey(req3)
	if keyNoHeader != keyWithHeader {
		t.Errorf("forwarded header changed the key: %q vs %q", keyNoHeader, keyWithHeader)
	}

	// RemoteAddr without a port still yields a usable key.
	req4 := httptest.NewRequest("GET", "/api/artifacts", nil)
	req4.RemoteAddr = "no-port-here"
	if key, typ := rateLimitKey(req4); key != "no-port-here" || typ != "ip" {
		t.Errorf("no-port key = (%q,%q), want (no-port-here, ip)", key, typ)
	}
}
