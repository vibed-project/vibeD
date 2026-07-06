package egressauthz

import (
	"context"
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

// stubResolvesToBlocked swaps the package-level DNS-rebinding check and returns
// a restore func, so handler tests stay hermetic (no real DNS).
func stubResolvesToBlocked(f func(context.Context, string) (bool, error)) func() {
	orig := resolvesToBlocked
	resolvesToBlocked = f
	return func() { resolvesToBlocked = orig }
}

func TestHandlerAuthz(t *testing.T) {
	// Keep allow decisions independent of real DNS.
	defer stubResolvesToBlocked(func(context.Context, string) (bool, error) { return false, nil })()

	res := fakeResolver{
		"10.0.0.1": {"api.openai.com", "*.example.com"},
		"10.0.0.2": {}, // app with an empty allow-list (egress fully denied)
	}
	h := NewHandler(res, []string{"minio.vibed-system.svc"}, nil)
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

// TestHandlerAuthz_RebindDeny: an allow-listed hostname that resolves to a
// blocked (metadata/internal) range is denied even though Authorize permits the
// name — the DNS-rebinding defense-in-depth.
func TestHandlerAuthz_RebindDeny(t *testing.T) {
	defer stubResolvesToBlocked(func(_ context.Context, host string) (bool, error) {
		return host == "rebind.example.com", nil // this name resolves to 169.254.169.254
	})()

	res := fakeResolver{"10.0.0.1": {"rebind.example.com", "api.openai.com"}}
	srv := httptest.NewServer(NewHandler(res, nil, nil))
	defer srv.Close()

	get := func(host string) int {
		resp, err := http.Get(srv.URL + "/authz?src=10.0.0.1&host=" + host)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("rebind.example.com"); code != http.StatusForbidden {
		t.Errorf("rebinding host: got %d, want 403", code)
	}
	if code := get("api.openai.com"); code != http.StatusOK {
		t.Errorf("clean allow-listed host: got %d, want 200", code)
	}
}

// TestHandlerAuthz_AllowsPrivateHostsDeniesMetadata exercises the REAL narrow
// resolve check (no stub) via literal IPs. A private/RFC1918 destination — like
// the in-cluster served source store's ClusterIP — must be ALLOWED (denying it
// 403'd the source pull and hung deploys in the cluster e2e), while a
// link-local metadata literal is still denied.
func TestHandlerAuthz_AllowsPrivateHostsDeniesMetadata(t *testing.T) {
	res := fakeResolver{"10.0.0.1": {"10.0.0.5", "169.254.169.254"}}
	srv := httptest.NewServer(NewHandler(res, []string{"10.0.0.9"}, nil))
	defer srv.Close()

	get := func(host string) int {
		resp, err := http.Get(srv.URL + "/authz?src=10.0.0.1&host=" + host)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("10.0.0.5"); code != http.StatusOK {
		t.Errorf("allow-listed private host: got %d, want 200", code)
	}
	if code := get("10.0.0.9"); code != http.StatusOK {
		t.Errorf("private system host (e.g. served store ClusterIP): got %d, want 200", code)
	}
	if code := get("169.254.169.254"); code != http.StatusForbidden {
		t.Errorf("allow-listed metadata literal: got %d, want 403", code)
	}
}
