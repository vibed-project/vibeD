package vibedhttp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHandler is a stand-in for the real health/ready/metrics handlers; it
// records the path it saw so tests can verify routing dispatched correctly.
type fakeHandler struct {
	gotPath string
	body    string
	status  int
}

func (f *fakeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.gotPath = r.URL.Path
	if f.status == 0 {
		f.status = http.StatusOK
	}
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

// newRouter wires Server onto a fresh mux exactly the way main.go does.
func newRouter(t *testing.T) (*http.ServeMux, *fakeHandler, *fakeHandler, *fakeHandler) {
	t.Helper()
	health := &fakeHandler{body: "alive"}
	ready := &fakeHandler{body: "ready"}
	metrics := &fakeHandler{body: "# HELP foo bar"}
	srv := New(health, ready, metrics, nil)
	mux := http.NewServeMux()
	HandlerFromMux(srv, mux)
	return mux, health, ready, metrics
}

func TestOpsEndpointsDispatch(t *testing.T) {
	mux, health, ready, metrics := newRouter(t)

	cases := []struct {
		path    string
		handler *fakeHandler
		want    string
	}{
		{"/healthz", health, "alive"},
		{"/readyz", ready, "ready"},
		{"/metrics", metrics, "# HELP foo bar"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			tc.handler.gotPath = ""
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200", rec.Code)
			}
			if tc.handler.gotPath != tc.path {
				t.Errorf("delegate not called: gotPath=%q", tc.handler.gotPath)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListTemplatesReturnsEmptyList(t *testing.T) {
	mux, _, _, _ := newRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/templates", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q, want application/json", ct)
	}

	var body struct {
		Items []Template `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Items == nil {
		t.Errorf("items must be a non-nil empty list, not null — clients rely on iterability")
	}
	if len(body.Items) != 0 {
		t.Errorf("expected empty list pre-milestone-C, got %d items", len(body.Items))
	}
}

func TestStubbedEndpointsReturn501(t *testing.T) {
	mux, _, _, _ := newRouter(t)

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/deploy"},
		{http.MethodGet, "/v1/apps"},
		{http.MethodGet, "/v1/apps/some-app"},
		{http.MethodDelete, "/v1/apps/some-app"},
		{http.MethodPost, "/v1/apps/some-app/redeploy"},
		{http.MethodPost, "/v1/apps/some-app/suspend"},
		{http.MethodGet, "/v1/apps/some-app/logs"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status: got %d, want 501", rec.Code)
			}
			var e Error
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("unmarshal Error: %v", err)
			}
			if !strings.HasSuffix(e.Code, "_not_implemented") {
				t.Errorf("error code should end with _not_implemented, got %q", e.Code)
			}
		})
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	mux, _, _, _ := newRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/not-a-real-endpoint", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
}
