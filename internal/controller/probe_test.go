package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/runneragent"
)

// TestProbeReadyOn2xx confirms a healthy agent → bound=true, no error.
func TestProbeReadyOn2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runneragent.PathHealthz {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := strings.TrimPrefix(srv.URL, "http://")
	probe := NewHTTPAgentProbe("")
	ready, err := probe.IsReady(context.Background(), target)
	if err != nil {
		t.Fatalf("IsReady: %v", err)
	}
	if !ready {
		t.Error("ready should be true on 2xx")
	}
}

// TestProbeNotReadyOn5xx returns ready=false + a non-nil err so the
// reconciler surfaces the cause via the condition message rather than
// silently treating it as "still booting".
func TestProbeNotReadyOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	target := strings.TrimPrefix(srv.URL, "http://")
	probe := NewHTTPAgentProbe("")
	ready, err := probe.IsReady(context.Background(), target)
	if ready {
		t.Error("ready should be false on 5xx")
	}
	if err == nil {
		t.Error("err should be set on 5xx")
	}
}

// TestProbeTimeout makes sure a slow agent doesn't stall reconciles past
// the configured timeout.
func TestProbeTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := strings.TrimPrefix(srv.URL, "http://")
	probe := &HTTPAgentProbe{Timeout: 50 * time.Millisecond}

	start := time.Now()
	ready, err := probe.IsReady(context.Background(), target)
	elapsed := time.Since(start)

	if ready {
		t.Error("ready should be false on timeout")
	}
	if err == nil {
		t.Error("err should be set on timeout")
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("probe took %v, well beyond the 50ms timeout — context deadline isn't propagating", elapsed)
	}
}

func TestProbeRejectsEmptyTarget(t *testing.T) {
	probe := NewHTTPAgentProbe("")
	_, err := probe.IsReady(context.Background(), "")
	if err == nil {
		t.Fatal("empty target should error")
	}
}
