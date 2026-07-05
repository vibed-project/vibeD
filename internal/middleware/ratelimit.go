// Package middleware provides HTTP middleware for vibeD.
package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"

	"golang.org/x/time/rate"
)

const maxRateLimitClients = 50000

// rateLimitKey returns the per-client rate-limit bucket key and its type label.
// It uses the authenticated user identity from the request context when present
// — in oauth-passthrough mode that is the trusted-proxy-validated identity, NOT
// the raw X-Forwarded-User header — and otherwise falls back to the socket peer
// address (r.RemoteAddr). It deliberately reads no X-Forwarded-* headers, so a
// client cannot forge or evade a limit by setting a forwarded identity header.
func rateLimitKey(r *http.Request) (key, clientType string) {
	if uid := vibedauth.UserIDFromContext(r.Context()); uid != "" {
		return uid, "apikey"
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	return host, "ip"
}

// RateLimiter returns HTTP middleware that rate-limits requests per client.
// Clients are identified by authenticated user ID (if available) or by IP address.
// Only /api/ and /mcp paths are rate-limited; health, metrics, and static are skipped.
// The ctx parameter controls the cleanup goroutine lifetime.
func RateLimiter(ctx context.Context, cfg config.RateLimitConfig, m *metrics.Metrics) func(http.Handler) http.Handler {
	// Per-client and per-share-token LRU caches. Eviction at capacity is O(1)
	// (least-recently-used at the list back), so a full cache no longer causes a
	// per-request O(n) scan under the lock (#62).
	clients := newLimiterLRU(maxRateLimitClients, rate.Limit(cfg.RequestsPerSecond), cfg.Burst)
	tokenClients := newLimiterLRU(maxRateLimitClients, rate.Every(time.Minute/5), 5) // 5 per minute

	// Periodically clean up stale entries; stops when ctx is cancelled.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				clients.evictStale(10 * time.Minute)
				tokenClients.evictStale(10 * time.Minute)
			}
		}
	}()

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
					if !tokenClients.get(token).Allow() {
						m.RateLimitedTotal.WithLabelValues("token").Inc()
						w.Header().Set("Retry-After", "12")
						http.Error(w, "rate limit exceeded for this share link", http.StatusTooManyRequests)
						return
					}
				}
			}

			// Determine the client key. IMPORTANT: never derive it from an
			// unauthenticated identity header (e.g. X-Forwarded-User) — in
			// oauth-passthrough mode that header is only trusted by the auth
			// verifier when it comes from a configured trusted proxy, and the
			// resulting identity is what UserIDFromContext returns. Keying on the
			// raw header would let a client evade or forge another user's limit.
			key, clientType := rateLimitKey(r)

			if !clients.get(key).Allow() {
				m.RateLimitedTotal.WithLabelValues(clientType).Inc()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
