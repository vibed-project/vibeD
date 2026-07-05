package middleware

import (
	"container/list"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// limiterLRU is a fixed-capacity, LRU cache of per-client rate limiters. The
// previous implementation evicted by scanning ALL entries under the write lock
// to find the oldest (O(n) with n up to 50k) on every new client once full — a
// latency spike per request at capacity (#62). This keeps entries in a
// recency-ordered list so eviction is O(1): the least-recently-used entry is at
// the back.
type limiterLRU struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List               // front = most recently used
	items    map[string]*list.Element // key → element holding *lruEntry
	newRate  rate.Limit
	newBurst int
}

type lruEntry struct {
	key      string
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newLimiterLRU(capacity int, r rate.Limit, burst int) *limiterLRU {
	if capacity < 1 {
		capacity = 1
	}
	return &limiterLRU{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		newRate:  r,
		newBurst: burst,
	}
}

// get returns the limiter for key, creating it (and evicting the LRU entry if at
// capacity) when absent. It marks the entry most-recently-used. O(1).
func (c *limiterLRU) get(key string) *rate.Limiter {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*lruEntry)
		e.lastSeen = time.Now()
		return e.limiter
	}

	// Evict the least-recently-used entry if at capacity. O(1).
	if c.ll.Len() >= c.capacity {
		if back := c.ll.Back(); back != nil {
			c.ll.Remove(back)
			delete(c.items, back.Value.(*lruEntry).key)
		}
	}

	e := &lruEntry{key: key, limiter: rate.NewLimiter(c.newRate, c.newBurst), lastSeen: time.Now()}
	c.items[key] = c.ll.PushFront(e)
	return e.limiter
}

// evictStale drops entries not seen within maxIdle. Runs O(n) but only on the
// periodic cleanup tick, never on the request path.
func (c *limiterLRU) evictStale(maxIdle time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-maxIdle)
	for el := c.ll.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*lruEntry)
		// The list is recency-ordered (LRU at the back); once we reach an entry
		// newer than the cutoff, everything ahead is newer too — stop early.
		if e.lastSeen.After(cutoff) {
			break
		}
		c.ll.Remove(el)
		delete(c.items, e.key)
		el = prev
	}
}

// len reports the current entry count (for tests).
func (c *limiterLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
