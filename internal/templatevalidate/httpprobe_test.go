package templatevalidate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The validator loop hands Info the manager's long-lived context, so the
// probe must impose its own per-call deadline — otherwise one wedged pod
// stalls the whole validation pass.
func TestHTTPInfoProbeAppliesDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// A wedged agent that never answers in time. Returns on client abort
		// so srv.Close (which waits for in-flight handlers) stays fast.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	target := strings.TrimPrefix(srv.URL, "http://")

	p := HTTPInfoProbe{Timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := p.Info(context.Background(), target)
	if err == nil {
		t.Fatal("Info against a wedged agent should time out")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded in the chain", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %v, want ~100ms", elapsed)
	}
}
