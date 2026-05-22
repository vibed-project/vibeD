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

	var tokenMu sync.RWMutex
	tokenClients := make(map[string]*client)

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

				tokenMu.Lock()
				for key, c := range tokenClients {
					if time.Since(c.lastSeen) > 10*time.Minute {
						delete(tokenClients, key)
					}
				}
				tokenMu.Unlock()
			}
		}
	}()

	getLimiter := func(key string, isToken bool) *rate.Limiter {
		var m *sync.RWMutex
		var cmap map[string]*client
		var r rate.Limit
		var b int

		if isToken {
			m = &tokenMu
			cmap = tokenClients
			r = rate.Every(time.Minute / 5) // 5 per minute
			b = 5
		} else {
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

		// Evict oldest entry if at capacity
		if len(cmap) >= maxRateLimitClients {
			var oldestKey string
			var oldestTime time.Time
			for k, c := range cmap {
				if oldestKey == "" || c.lastSeen.Before(oldestTime) {
					oldestKey = k
					oldestTime = c.lastSeen
				}
			}
			delete(cmap, oldestKey)
		}

		limiter := rate.NewLimiter(r, b)
		cmap[key] = &client{limiter: limiter, lastSeen: time.Now()}
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

			// Add strict per-token rate limit for share link password attempts
			if r.Method == http.MethodPost && strings.HasPrefix(path, "/api/share/") {
				token := strings.TrimPrefix(path, "/api/share/")
				if token != "" {
					if !getLimiter(token, true).Allow() {
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

			if !getLimiter(key, false).Allow() {
				m.RateLimitedTotal.WithLabelValues(clientType).Inc()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
