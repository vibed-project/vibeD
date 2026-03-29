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

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter returns HTTP middleware that rate-limits requests per client.
// Clients are identified by authenticated user ID (if available) or by IP address.
// Only /api/ and /mcp paths are rate-limited; health, metrics, and static are skipped.
// The ctx parameter controls the cleanup goroutine lifetime.
func RateLimiter(ctx context.Context, cfg config.RateLimitConfig, m *metrics.Metrics) func(http.Handler) http.Handler {
	var mu sync.RWMutex
	clients := make(map[string]*client)

	// Periodically clean up stale entries; stops when ctx is cancelled.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				for key, c := range clients {
					if time.Since(c.lastSeen) > 10*time.Minute {
						delete(clients, key)
					}
				}
				mu.Unlock()
			}
		}
	}()

	getLimiter := func(key string) *rate.Limiter {
		// Fast path: check if client exists with read lock
		mu.RLock()
		if c, ok := clients[key]; ok {
			c.lastSeen = time.Now()
			mu.RUnlock()
			return c.limiter
		}
		mu.RUnlock()

		// Slow path: create new limiter with write lock
		mu.Lock()
		defer mu.Unlock()

		// Double-check after acquiring write lock
		if c, ok := clients[key]; ok {
			c.lastSeen = time.Now()
			return c.limiter
		}

		// Evict oldest entry if at capacity
		if len(clients) >= maxRateLimitClients {
			var oldestKey string
			var oldestTime time.Time
			for k, c := range clients {
				if oldestKey == "" || c.lastSeen.Before(oldestTime) {
					oldestKey = k
					oldestTime = c.lastSeen
				}
			}
			delete(clients, oldestKey)
		}

		limiter := rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), cfg.Burst)
		clients[key] = &client{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Only rate-limit API and MCP paths
			if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/mcp") {
				next.ServeHTTP(w, r)
				return
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

			if !getLimiter(key).Allow() {
				m.RateLimitedTotal.WithLabelValues(clientType).Inc()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
