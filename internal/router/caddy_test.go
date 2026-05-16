package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeCaddy is a stand-in for the Caddy admin API. It implements the
// minimal /id/* and /config/apps/http/servers/srv0/routes paths the client
// uses, plus a behavior knob (RouteExists) so tests can exercise both the
// PATCH-existing and POST-append branches of EnsureRoute.
type fakeCaddy struct {
	mu        sync.Mutex
	routes    map[string]json.RawMessage // @id → route body
	patchHits int
	postHits  int
	deleteHit int
}

func newFakeCaddy() *fakeCaddy {
	return &fakeCaddy{routes: map[string]json.RawMessage{}}
}

func (f *fakeCaddy) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/id/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/id/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPatch:
			f.patchHits++
			if _, ok := f.routes[id]; !ok {
				http.NotFound(w, r)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.routes[id] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			f.deleteHit++
			if _, ok := f.routes[id]; !ok {
				http.NotFound(w, r)
				return
			}
			delete(f.routes, id)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/config/apps/http/servers/srv0/routes", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			ids := make([]map[string]string, 0, len(f.routes))
			for id := range f.routes {
				ids = append(ids, map[string]string{"@id": id})
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i]["@id"] < ids[j]["@id"] })
			_ = json.NewEncoder(w).Encode(ids)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// POST /config/apps/http/servers/srv0/routes/... is what EnsureRoute
	// falls back to when PATCH 404s. Caddy supports `/...` to append.
	mux.HandleFunc("/config/apps/http/servers/srv0/routes/...", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.postHits++

		// Parse out the @id so we can key on it in the fake's routes map.
		body, _ := io.ReadAll(r.Body)
		var probe struct {
			ID string `json:"@id"`
		}
		if err := json.Unmarshal(body, &probe); err != nil || probe.ID == "" {
			http.Error(w, "missing @id", http.StatusBadRequest)
			return
		}
		f.routes[probe.ID] = body
		w.WriteHeader(http.StatusOK)
	})

	return mux
}

func TestEnsureRouteCreatesViaPOSTWhenAbsent(t *testing.T) {
	fc := newFakeCaddy()
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := NewCaddyClient(srv.URL)
	r := Route{ID: "vibed-app/abc", Host: "abc.vibed.example.com", Upstream: "10.0.0.1:8080"}
	if err := c.EnsureRoute(context.Background(), r); err != nil {
		t.Fatalf("EnsureRoute: %v", err)
	}
	if fc.patchHits != 1 {
		t.Errorf("patchHits = %d, expected 1 (first try is PATCH)", fc.patchHits)
	}
	if fc.postHits != 1 {
		t.Errorf("postHits = %d, expected 1 (fall back to POST when PATCH 404)", fc.postHits)
	}
	if _, ok := fc.routes[r.ID]; !ok {
		t.Errorf("route %q not stored", r.ID)
	}
}

func TestEnsureRouteReplacesViaPATCHWhenPresent(t *testing.T) {
	fc := newFakeCaddy()
	// Seed the route so PATCH succeeds.
	fc.routes["vibed-app/abc"] = json.RawMessage(`{"@id":"vibed-app/abc"}`)
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := NewCaddyClient(srv.URL)
	r := Route{ID: "vibed-app/abc", Host: "abc.vibed.example.com", Upstream: "10.0.0.99:8080"}
	if err := c.EnsureRoute(context.Background(), r); err != nil {
		t.Fatalf("EnsureRoute: %v", err)
	}
	if fc.patchHits != 1 {
		t.Errorf("patchHits = %d, expected 1", fc.patchHits)
	}
	if fc.postHits != 0 {
		t.Errorf("postHits = %d, expected 0 (PATCH succeeded; no fallback)", fc.postHits)
	}
	// Body should reflect the new upstream.
	if !strings.Contains(string(fc.routes[r.ID]), "10.0.0.99:8080") {
		t.Errorf("route body did not capture new upstream: %s", string(fc.routes[r.ID]))
	}
}

func TestDeleteRouteRemovesAndIsIdempotent(t *testing.T) {
	fc := newFakeCaddy()
	fc.routes["vibed-app/abc"] = json.RawMessage(`{"@id":"vibed-app/abc"}`)
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := NewCaddyClient(srv.URL)
	if err := c.DeleteRoute(context.Background(), "vibed-app/abc"); err != nil {
		t.Fatalf("first DeleteRoute: %v", err)
	}
	// Second delete: route is already gone, Caddy returns 404, client
	// treats that as success.
	if err := c.DeleteRoute(context.Background(), "vibed-app/abc"); err != nil {
		t.Fatalf("second DeleteRoute (already gone): %v", err)
	}
	if _, ok := fc.routes["vibed-app/abc"]; ok {
		t.Error("route should be deleted")
	}
}

func TestListRouteIDs(t *testing.T) {
	fc := newFakeCaddy()
	fc.routes["vibed-app/a"] = json.RawMessage(`{"@id":"vibed-app/a"}`)
	fc.routes["vibed-app/b"] = json.RawMessage(`{"@id":"vibed-app/b"}`)
	fc.routes["other"] = json.RawMessage(`{"@id":"other"}`)
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	c := NewCaddyClient(srv.URL)
	ids, err := c.ListRouteIDs(context.Background())
	if err != nil {
		t.Fatalf("ListRouteIDs: %v", err)
	}
	sort.Strings(ids)
	want := []string{"other", "vibed-app/a", "vibed-app/b"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range ids {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

func TestEnsureRouteValidatesInput(t *testing.T) {
	c := NewCaddyClient("http://unused")
	for _, tc := range []struct {
		name string
		r    Route
	}{
		{"empty ID", Route{Host: "h", Upstream: "u"}},
		{"empty Host", Route{ID: "i", Upstream: "u"}},
		{"empty Upstream", Route{ID: "i", Host: "h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.EnsureRoute(context.Background(), tc.r); err == nil {
				t.Errorf("expected validation error for %+v", tc.r)
			}
		})
	}
}
