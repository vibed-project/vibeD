package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"
)

// serveN sends n requests (method, path) through the rate-limit middleware and
// returns how many were allowed (200) vs limited (429).
func serveN(t *testing.T, mw func(http.Handler) http.Handler, method, path string, n int) (allowed, limited int) {
	t.Helper()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "203.0.113.7:5555" // stable IP key
		h.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusOK:
			allowed++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	return allowed, limited
}

// TestRateLimitCoversV1AndWriteClass locks in issue #50: /v1/* is rate-limited
// at all, and mutating verbs draw from the stricter write budget (so a burst of
// deploys is cut off well before a burst of reads would be).
func TestRateLimitCoversV1AndWriteClass(t *testing.T) {
	m := metrics.New()
	cfg := config.RateLimitConfig{
		Enabled:                true,
		RequestsPerSecond:      1,
		Burst:                  100, // generous read budget
		WriteRequestsPerSecond: 1,
		WriteBurst:             3, // tight write budget
	}
	mw := RateLimiter(context.Background(), cfg, m)

	// /v1 is now covered: writes beyond the write burst are limited.
	allowed, limited := serveN(t, mw, http.MethodPost, "/v1/deploy", 10)
	if limited == 0 {
		t.Fatalf("POST /v1/deploy should be rate-limited past the write burst; allowed=%d limited=%d", allowed, limited)
	}
	if allowed > cfg.WriteBurst {
		t.Errorf("allowed %d writes, want <= writeBurst %d", allowed, cfg.WriteBurst)
	}

	// Reads on /v1 draw from the generous general budget — the earlier write
	// throttling must not have consumed it.
	if a, l := serveN(t, mw, http.MethodGet, "/v1/apps", 50); a != 50 || l != 0 {
		t.Errorf("GET /v1/apps under general budget: allowed=%d limited=%d, want 50/0", a, l)
	}
}

// TestRateLimitSkipsNonAPIPaths confirms health/static paths are still exempt.
func TestRateLimitSkipsNonAPIPaths(t *testing.T) {
	m := metrics.New()
	mw := RateLimiter(context.Background(), config.RateLimitConfig{Enabled: true, RequestsPerSecond: 1, Burst: 1}, m)
	if a, l := serveN(t, mw, http.MethodGet, "/healthz", 20); a != 20 || l != 0 {
		t.Errorf("/healthz should be exempt: allowed=%d limited=%d, want 20/0", a, l)
	}
}

// TestEvictOldestSampled covers the bounded-sample eviction added for issue #62.
func TestEvictOldestSampled(t *testing.T) {
	now := time.Now()

	t.Run("empty map is a no-op", func(t *testing.T) {
		cmap := map[string]*client{}
		evictOldestSampled(cmap) // must not panic
		if len(cmap) != 0 {
			t.Fatalf("len = %d, want 0", len(cmap))
		}
	})

	t.Run("evicts the true oldest when map fits in the sample", func(t *testing.T) {
		// With <= rateLimitEvictSample entries, every entry is scanned, so the
		// least-recently-seen one is deterministically evicted.
		cmap := map[string]*client{
			"new":    {lastSeen: now},
			"oldest": {lastSeen: now.Add(-1 * time.Hour)},
			"mid":    {lastSeen: now.Add(-30 * time.Minute)},
		}
		evictOldestSampled(cmap)
		if _, ok := cmap["oldest"]; ok {
			t.Fatalf("oldest entry should have been evicted; map=%v", keys(cmap))
		}
		if len(cmap) != 2 {
			t.Fatalf("len = %d, want 2", len(cmap))
		}
	})

	t.Run("evicts exactly one entry on a large map", func(t *testing.T) {
		cmap := make(map[string]*client, rateLimitEvictSample*4)
		for i := 0; i < rateLimitEvictSample*4; i++ {
			cmap[string(rune('a'+i%26))+time.Duration(i).String()] = &client{lastSeen: now.Add(-time.Duration(i) * time.Second)}
		}
		before := len(cmap)
		evictOldestSampled(cmap)
		if len(cmap) != before-1 {
			t.Fatalf("len = %d, want %d (exactly one evicted)", len(cmap), before-1)
		}
	})
}

func keys(m map[string]*client) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
