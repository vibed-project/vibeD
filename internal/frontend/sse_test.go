package frontend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/metrics"
)

// TestSSEDoesNotSetConnectionHeader locks in the #41 fix: the SSE handler must
// not set the hop-by-hop "Connection" header, which is forbidden under HTTP/2
// and previously broke the event stream (and thus the login form) over h2.
func TestSSEDoesNotSetConnectionHeader(t *testing.T) {
	bus := events.NewEventBus()
	h := handleSSE(bus, metrics.New())

	rec := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)

	h(rec, req) // returns when ctx times out

	if got := rec.Header().Get("Connection"); got != "" {
		t.Errorf("SSE handler set Connection header %q; must be unset (forbidden under HTTP/2)", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// TestSSEWorksOverHTTP2 drives the SSE handler over a real HTTP/2 (TLS) server
// and asserts the request negotiates h2 and returns 200 — i.e. the response is
// not rejected for an illegal hop-by-hop header.
func TestSSEWorksOverHTTP2(t *testing.T) {
	bus := events.NewEventBus()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", handleSSE(bus, metrics.New()))

	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client := srv.Client() // trusts the test server's TLS cert
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP/2 SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2, got %s", resp.Proto)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE over HTTP/2 status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	// Read one heartbeat/chunk to confirm the stream is actually writable over
	// h2 (a forbidden header would have failed the response before this).
	buf := make([]byte, 1)
	_, _ = resp.Body.Read(buf) // best-effort; ctx bounds the wait
}
