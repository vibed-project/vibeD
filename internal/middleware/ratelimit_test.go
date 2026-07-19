package middleware

import (
	"testing"
	"time"
)

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
