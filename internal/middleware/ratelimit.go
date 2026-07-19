// Package middleware provides HTTP middleware for vibeD.
package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"

	"golang.org/x/time/rate"
)

const maxRateLimitClients = 50000

// Fallback write-verb limits when RateLimitConfig leaves them unset (<=0), so a
// partial config can't accidentally leave the deploy path unlimited.
const (
	defaultWriteRPS   = 1.0
	defaultWriteBurst = 5
)

// limiterClass selects which per-client token-bucket pool a request draws from.
type limiterClass int

const (
	classGeneral limiterClass = iota // reads / general API traffic
	classToken                       // per-share-link password attempts
	classWrite                       // expensive mutating verbs (deploy/create/update/delete)
)

// isWriteVerb reports whether method is a mutating HTTP verb that should draw
// from the stricter write budget.
func isWriteVerb(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// rateLimitEvictSample bounds the work of a single eviction: we look at this
// many entries and drop the least-recently-seen among them. Redis uses a
// similar sampled-LRU with a sample of ~5-10; 16 tightens the approximation
// while keeping eviction O(1) with respect to map size.
const rateLimitEvictSample = 16

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// evictOldestSampled deletes the least-recently-seen of up to
// rateLimitEvictSample entries (all of them when the map is smaller than the
// sample). Cost is bounded by the sample size, not len(cmap). Caller holds the
// write lock. A no-op on an empty map.
func evictOldestSampled(cmap map[string]*client) {
	var oldestKey string
	var oldestTime time.Time
	seen := 0
	for k, c := range cmap {
		if oldestKey == "" || c.lastSeen.Before(oldestTime) {
			oldestKey = k
			oldestTime = c.lastSeen
		}
		if seen++; seen >= rateLimitEvictSample {
			break
		}
	}
	if oldestKey != "" {
		delete(cmap, oldestKey)
	}
}

// RateLimiter returns HTTP middleware that rate-limits requests per client.
// Clients are identified by authenticated user ID (if available) or by IP address.
// Only /api/ and /mcp paths are rate-limited; health, metrics, and static are skipped.
// The ctx parameter controls the cleanup goroutine lifetime.
func RateLimiter(ctx context.Context, cfg config.RateLimitConfig, m *metrics.Metrics) func(http.Handler) http.Handler {
	// Write verbs get their own tighter budget; fall back to a safe default when
	// unset so a partial config can't accidentally leave them unlimited.
	writeRPS := cfg.WriteRequestsPerSecond
	if writeRPS <= 0 {
		writeRPS = defaultWriteRPS
	}
	writeBurst := cfg.WriteBurst
	if writeBurst <= 0 {
		writeBurst = defaultWriteBurst
	}

	var mu sync.RWMutex
	clients := make(map[string]*client)

	var tokenMu sync.RWMutex
	tokenClients := make(map[string]*client)

	var writeMu sync.RWMutex
	writeClients := make(map[string]*client)

	// Periodically clean up stale entries; stops when ctx is cancelled.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		sweep := func(mu *sync.RWMutex, cmap map[string]*client) {
			mu.Lock()
			defer mu.Unlock()
			for key, c := range cmap {
				if time.Since(c.lastSeen) > 10*time.Minute {
					delete(cmap, key)
				}
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep(&mu, clients)
				sweep(&tokenMu, tokenClients)
				sweep(&writeMu, writeClients)
			}
		}
	}()

	getLimiter := func(key string, class limiterClass) *rate.Limiter {
		var m *sync.RWMutex
		var cmap map[string]*client
		var r rate.Limit
		var b int

		switch class {
		case classToken:
			m = &tokenMu
			cmap = tokenClients
			r = rate.Every(time.Minute / 5) // 5 per minute
			b = 5
		case classWrite:
			m = &writeMu
			cmap = writeClients
			r = rate.Limit(writeRPS)
			b = writeBurst
		default: // classGeneral
			m = &mu
			cmap = clients
			r = rate.Limit(cfg.RequestsPerSecond)
			b = cfg.Burst
		}

		// Fast path: check if client exists with read lock
		m.RLock()
		if c, ok := cmap[key]; ok {
			c.lastSeen = time.Now()
			m.RUnlock()
			return c.limiter
		}
		m.RUnlock()

		// Slow path: create new limiter with write lock
		m.Lock()
		defer m.Unlock()

		// Double-check after acquiring write lock
		if c, ok := cmap[key]; ok {
			c.lastSeen = time.Now()
			return c.limiter
		}

		// Evict one entry if at capacity. We sample a bounded number of entries
		// and drop the oldest of the sample (approximate LRU) rather than scanning
		// the whole map: a full O(n) scan under the write lock let an attacker
		// rotating keys thrash eviction and degrade throughput for every client
		// (issue #62). Go randomizes map iteration, so a small sample is a
		// good-enough oldest-estimate for a cache this size.
		if len(cmap) >= maxRateLimitClients {
			evictOldestSampled(cmap)
		}

		limiter := rate.NewLimiter(r, b)
		cmap[key] = &client{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Rate-limit the API surfaces (REST /api, deploy/read /v1, and MCP);
			// health, metrics, and static assets are skipped. /v1/* — including
			// the expensive deploy path — was previously unthrottled (#50).
			if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/mcp") && !strings.HasPrefix(path, "/v1/") {
				next.ServeHTTP(w, r)
				return
			}

			// Add strict per-token rate limit for share link password attempts
			if r.Method == http.MethodPost && strings.HasPrefix(path, "/api/share/") {
				token := strings.TrimPrefix(path, "/api/share/")
				if token != "" {
					if !getLimiter(token, classToken).Allow() {
						m.RateLimitedTotal.WithLabelValues("token").Inc()
						w.Header().Set("Retry-After", "12")
						http.Error(w, "rate limit exceeded for this share link", http.StatusTooManyRequests)
						return
					}
				}
			}

			// Determine client key: authenticated user ID or IP
			key := vibedauth.UserIDFromContext(r.Context())
			clientType := "apikey"
			if key == "" {
				clientType = "ip"
				key, _, _ = net.SplitHostPort(r.RemoteAddr)
				if key == "" {
					key = r.RemoteAddr
				}
			}

			// Expensive mutating REST verbs (deploy/create/update/delete on /v1
			// or /api) draw from a stricter per-client budget so one user can't
			// flood the deploy path. MCP stays on the general limiter — it's
			// JSON-RPC over POST, so the HTTP method doesn't distinguish reads
			// from writes there.
			class := classGeneral
			if isWriteVerb(r.Method) && (strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/api/")) {
				class = classWrite
			}

			if !getLimiter(key, class).Allow() {
				m.RateLimitedTotal.WithLabelValues(clientType).Inc()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
