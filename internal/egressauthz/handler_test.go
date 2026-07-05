package egressauthz

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeResolver maps src IP → allow-list.
type fakeResolver map[string][]string

func (f fakeResolver) AllowedFor(_ context.Context, src string) ([]string, bool) {
	h, ok := f[src]
	return h, ok
}

// fakeDNS maps a hostname → resolved IPs so tests never touch real DNS. A host
// absent from the map resolves to a benign public IP (so allow-listed hosts
// stay allowed by default).
type fakeDNS map[string][]string

func (f fakeDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := f[host]
	if !ok {
		ips = []string{"93.184.216.34"} // example.com, a benign public IP
	}
	addrs := make([]net.IPAddr, 0, len(ips))
	for _, s := range ips {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(s)})
	}
	return addrs, nil
}

// newTestHandler wires a Handler with injected allow-list resolver and DNS so
// the private-IP deny path is exercised deterministically, offline.
func newTestHandler(res Resolver, systemHosts, allowInternalHosts []string, dns hostResolver) http.Handler {
	h := &Handler{
		resolver:           res,
		systemHosts:        systemHosts,
		logger:             nil,
		dns:                dns,
		allowInternalHosts: allowInternalHosts,
	}
	if h.logger == nil {
		h.logger = discardLogger()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authz", h.authz)
	return mux
}

func TestHandlerAuthz(t *testing.T) {
	res := fakeResolver{
		"10.0.0.1": {"api.openai.com", "*.example.com"},
		"10.0.0.2": {}, // app with an empty allow-list (egress fully denied)
	}
	h := newTestHandler(res, []string{"minio.vibed-system.svc"}, nil, fakeDNS{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	check := func(src, host string, wantCode int) {
		t.Helper()
		resp, err := http.Get(srv.URL + "/authz?src=" + src + "&host=" + host)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != wantCode {
			t.Errorf("authz(src=%s host=%s) = %d, want %d", src, host, resp.StatusCode, wantCode)
		}
	}

	check("10.0.0.1", "api.openai.com", http.StatusOK)         // allow-listed
	check("10.0.0.1", "a.example.com", http.StatusOK)          // wildcard
	check("10.0.0.1", "evil.net", http.StatusForbidden)        // not listed
	check("10.0.0.1", "minio.vibed-system.svc", http.StatusOK) // system host
	check("10.0.0.2", "api.openai.com", http.StatusForbidden)  // empty allow-list
	check("10.0.0.2", "minio.vibed-system.svc", http.StatusOK) // system still allowed
	check("10.9.9.9", "api.openai.com", http.StatusForbidden)  // unknown source
	check("10.9.9.9", "minio.vibed-system.svc", http.StatusOK) // unknown source, system host
}

// TestHandlerDeniesPrivateIP proves the DNS-rebind defense: an allow-listed
// host that resolves to a private/link-local/loopback/metadata range is denied
// regardless of the allow-list, while a normal public host stays allowed.
func TestHandlerDeniesPrivateIP(t *testing.T) {
	res := fakeResolver{
		"10.0.0.1": {"*.example.com", "metadata.evil.com", "internal.evil.com"},
	}
	dns := fakeDNS{
		"metadata.evil.com": {"169.254.169.254"}, // link-local instance metadata endpoint
		"internal.evil.com": {"10.1.2.3"},        // internal RFC1918
		"loop.evil.com":     {"127.0.0.1"},       // loopback
		"v6loop.evil.com":   {"::1"},             // IPv6 loopback
		"ula.evil.com":      {"fd00::1"},         // IPv6 unique-local
		"good.example.com":  {"93.184.216.34"},   // benign public
	}
	h := newTestHandler(res, nil, nil, dns)
	srv := httptest.NewServer(h)
	defer srv.Close()

	check := func(host string, wantCode int) {
		t.Helper()
		resp, err := http.Get(srv.URL + "/authz?src=10.0.0.1&host=" + host)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != wantCode {
			t.Errorf("authz(host=%s) = %d, want %d", host, resp.StatusCode, wantCode)
		}
	}

	// Allow-listed but resolves into a blocked range → DENY.
	check("metadata.evil.com", http.StatusForbidden)
	check("internal.evil.com", http.StatusForbidden)
	// Wildcard-matched but resolve to blocked ranges → DENY.
	check("loop.evil.com", http.StatusForbidden) // *.example.com does NOT match; not listed → deny anyway
	check("good.example.com", http.StatusOK)     // allow-listed via wildcard, public IP → ALLOW
	// A raw private IP passed as the host string is denied directly.
	check("169.254.169.254", http.StatusForbidden)
}

// TestHandlerAllowInternalOptOut proves the operator opt-out: a specific
// internal host can be permitted to resolve into an otherwise-blocked range.
func TestHandlerAllowInternalOptOut(t *testing.T) {
	res := fakeResolver{
		"10.0.0.1": {"internal-api.corp", "other-internal.corp"},
	}
	dns := fakeDNS{
		"internal-api.corp":   {"10.5.5.5"},
		"other-internal.corp": {"10.6.6.6"},
	}
	// Only internal-api.corp is opted out of the private-IP deny.
	h := newTestHandler(res, nil, []string{"internal-api.corp"}, dns)
	srv := httptest.NewServer(h)
	defer srv.Close()

	get := func(host string) int {
		resp, err := http.Get(srv.URL + "/authz?src=10.0.0.1&host=" + host)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("internal-api.corp"); code != http.StatusOK {
		t.Errorf("opted-out internal host = %d, want 200", code)
	}
	if code := get("other-internal.corp"); code != http.StatusForbidden {
		t.Errorf("non-opted-out internal host = %d, want 403", code)
	}
}
