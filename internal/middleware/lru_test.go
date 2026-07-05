package middleware

import (
	"strconv"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestLimiterLRUEvictsLeastRecentlyUsed is the proof for #62: the cache stays at
// capacity and evicts the LRU entry (not a random or O(n)-scanned one) when a
// new key arrives.
func TestLimiterLRUEvictsLeastRecentlyUsed(t *testing.T) {
	c := newLimiterLRU(3, rate.Limit(10), 5)

	a := c.get("a")
	c.get("b")
	c.get("c")
	if c.len() != 3 {
		t.Fatalf("len = %d, want 3", c.len())
	}

	// Touch "a" so it's most-recently-used; "b" is now the LRU.
	if c.get("a") != a {
		t.Error("get(a) should return the same limiter instance")
	}
	// Insert "d" → evicts "b" (the LRU), not "a".
	c.get("d")
	if c.len() != 3 {
		t.Fatalf("len after insert = %d, want 3 (capacity held)", c.len())
	}

	// "a", "c", "d" present; "b" evicted. We detect eviction by identity: a
	// re-get of an evicted key yields a fresh limiter, a live key yields the
	// same one.
	if c.get("a") != a {
		t.Error("a should still be cached (was most-recently-used)")
	}
	c.mu.Lock()
	_, bStillThere := c.items["b"]
	c.mu.Unlock()
	if bStillThere {
		t.Error("b should have been evicted as the least-recently-used entry")
	}
}

// TestLimiterLRUCapacityBound stresses the cache well past capacity and asserts
// it never exceeds capacity (the eviction path holds).
func TestLimiterLRUCapacityBound(t *testing.T) {
	const cap = 100
	c := newLimiterLRU(cap, rate.Limit(10), 5)
	for i := 0; i < 10*cap; i++ {
		c.get("key-" + strconv.Itoa(i))
		if got := c.len(); got > cap {
			t.Fatalf("cache exceeded capacity: len=%d cap=%d", got, cap)
		}
	}
	if c.len() != cap {
		t.Errorf("final len = %d, want %d", c.len(), cap)
	}
}

// TestLimiterLRUEvictStale drops idle entries and keeps fresh ones.
func TestLimiterLRUEvictStale(t *testing.T) {
	c := newLimiterLRU(10, rate.Limit(10), 5)
	c.get("old")
	// Backdate "old" so it looks idle.
	c.mu.Lock()
	c.items["old"].Value.(*lruEntry).lastSeen = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	c.get("fresh")

	c.evictStale(10 * time.Minute)

	c.mu.Lock()
	_, oldThere := c.items["old"]
	_, freshThere := c.items["fresh"]
	c.mu.Unlock()
	if oldThere {
		t.Error("stale 'old' entry should have been evicted")
	}
	if !freshThere {
		t.Error("fresh entry must be kept")
	}
}
