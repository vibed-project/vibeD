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

func TestHandlerAuthz(t *testing.T) {
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
