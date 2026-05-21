package workerd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// clientAgainst points a Client at a single httptest server, ignoring the
// shard math (1 replica) so we can exercise the request/response contract.
func clientAgainst(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	return &Client{
		Replicas:    1,
		ControlPort: portOf(t, u.Host),
		ReplicaHost: func(int) string { return hostOf(u.Host) },
		Token:       "tok",
	}
}

func TestClientDeployReturnsTarget(t *testing.T) {
	var gotBody DeployRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/deploy" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(DeployResponse{Port: 9137})
	}))
	defer srv.Close()

	c := clientAgainst(t, srv)
	target, err := c.Deploy(context.Background(), "myapp", "http://store/s.tgz", "worker.js")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if target.Port != 9137 {
		t.Errorf("port = %d, want 9137", target.Port)
	}
	if !strings.HasSuffix(target.Addr(), ":9137") {
		t.Errorf("addr = %q, want host:9137", target.Addr())
	}
	if gotBody.AppID != "myapp" || gotBody.SourceURL != "http://store/s.tgz" || gotBody.Entrypoint != "worker.js" {
		t.Errorf("request body mismatch: %+v", gotBody)
	}
}

func TestClientDeploySurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no entry module", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := clientAgainst(t, srv)
	_, err := c.Deploy(context.Background(), "app", "http://x", "")
	if err == nil || !strings.Contains(err.Error(), "no entry module") {
		t.Fatalf("expected surfaced 400 body, got %v", err)
	}
}

func TestClientRemoveTreats404AsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNotFound) // already gone
	}))
	defer srv.Close()

	c := clientAgainst(t, srv)
	if err := c.Remove(context.Background(), "app"); err != nil {
		t.Errorf("Remove should treat 404 as success, got %v", err)
	}
}

func TestShardIsStableAndInRange(t *testing.T) {
	c := &Client{Replicas: 4, ReplicaHost: func(int) string { return "h" }}
	first := c.shard("some-app")
	for i := 0; i < 10; i++ {
		if c.shard("some-app") != first {
			t.Fatal("shard not stable for the same id")
		}
	}
	if first < 0 || first >= 4 {
		t.Errorf("shard %d out of range [0,4)", first)
	}
	// Single replica always shards to 0.
	one := &Client{Replicas: 1, ReplicaHost: func(int) string { return "h" }}
	if one.shard("anything") != 0 {
		t.Error("single-replica shard must be 0")
	}
}

// --- host:port parsing helpers for the test ---

func hostOf(hostport string) string {
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[:i]
	}
	return hostport
}

func portOf(t *testing.T, hostport string) int {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		t.Fatalf("no port in %q", hostport)
	}
	var p int
	if _, err := fmt.Sscanf(hostport[i+1:], "%d", &p); err != nil {
		t.Fatalf("bad port in %q: %v", hostport, err)
	}
	return p
}
