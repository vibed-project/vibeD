package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vibed-project/vibeD/internal/store"
)

// Identity is the request-scoped view of the authenticated user, resolved ONCE
// per request right after token verification and flowed via context (see
// WithIdentity). The suspended-user check and RoleMiddleware read it instead of
// each doing their own user-store lookup, collapsing the 2-5 store queries a
// request used to cost into at most one.
type Identity struct {
	ID           string
	Name         string
	Email        string
	Role         string
	Status       string
	Provider     string
	DepartmentID string
}

// identityCacheMaxEntries bounds both the identity cache and the OIDC sync
// fingerprint cache so an attacker cycling identities can't grow them without
// bound (mirrors maxRateLimitClients in internal/middleware).
const identityCacheMaxEntries = 50000

// identityCacheEvictSample bounds the work of a single eviction, mirroring
// rateLimitEvictSample in internal/middleware/ratelimit.go: inspect this many
// entries and drop the one expiring soonest, keeping eviction O(1) with
// respect to map size.
const identityCacheEvictSample = 16

// ttlEntry is one ttlCache slot: a value and its absolute expiry.
type ttlEntry[V any] struct {
	val       V
	expiresAt time.Time
}

// ttlCache is a small bounded map with per-entry TTL (map+RWMutex+expiry). A
// ttl <= 0 disables it entirely: get always misses and put is a no-op, so
// callers degrade to hitting their backing store on every request.
type ttlCache[V any] struct {
	ttl time.Duration
	max int

	mu      sync.RWMutex
	entries map[string]ttlEntry[V]
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, max: max, entries: map[string]ttlEntry[V]{}}
}

// get returns the cached value for key when present and unexpired.
func (c *ttlCache[V]) get(key string) (V, bool) {
	var zero V
	if c.ttl <= 0 {
		return zero, false
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return zero, false
	}
	return e.val, true
}

// put stores val under key for the cache TTL, evicting a sampled
// soonest-to-expire entry when the size bound is hit.
func (c *ttlCache[V]) put(key string, val V) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		evictSoonestSampled(c.entries)
	}
	c.entries[key] = ttlEntry[V]{val: val, expiresAt: time.Now().Add(c.ttl)}
}

// evictSoonestSampled deletes the soonest-to-expire of up to
// identityCacheEvictSample entries (all of them when the map is smaller than
// the sample) — the TTL-cache analogue of ratelimit's evictOldestSampled. Cost
// is bounded by the sample size, not len(entries). Caller holds the write
// lock. A no-op on an empty map.
func evictSoonestSampled[V any](entries map[string]ttlEntry[V]) {
	var victim string
	var victimExpiry time.Time
	found := false
	seen := 0
	for k, e := range entries {
		if !found || e.expiresAt.Before(victimExpiry) {
			victim = k
			victimExpiry = e.expiresAt
			found = true
		}
		if seen++; seen >= identityCacheEvictSample {
			break
		}
	}
	if found {
		delete(entries, victim)
	}
}

// identityResolver turns an authenticated user ID into an Identity with at
// most one user-store lookup, fronted by a bounded TTL cache
// (auth.identityCacheTTL). The TTL is the propagation bound for account
// changes: a suspension or role edit takes effect within at most one TTL on a
// warm cache; ttl <= 0 disables caching so every request consults the store
// (exactly once).
type identityResolver struct {
	userStore store.UserStore
	cache     *ttlCache[Identity]
}

func newIdentityResolver(userStore store.UserStore, ttl time.Duration) *identityResolver {
	return &identityResolver{userStore: userStore, cache: newTTLCache[Identity](ttl, identityCacheMaxEntries)}
}

// Resolve returns userID's identity, from the cache when fresh, else the
// store. It returns nil when no store is wired or the store has no record —
// preserving today's behavior for unknown identities (no suspension check;
// role falls back elsewhere). Misses are NOT negatively cached, so a user
// provisioned a moment later is picked up on their next request.
func (rs *identityResolver) Resolve(ctx context.Context, userID string) *Identity {
	if rs == nil || rs.userStore == nil || userID == "" {
		return nil
	}
	if ident, ok := rs.cache.get(userID); ok {
		return &ident
	}
	u, err := rs.userStore.GetUser(ctx, userID)
	if err != nil || u == nil {
		return nil
	}
	ident := Identity{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		Role:         u.Role,
		Status:       u.Status,
		Provider:     u.Provider,
		DepartmentID: u.DepartmentID,
	}
	rs.cache.put(userID, ident)
	return &ident
}

// oidcSyncer wraps the per-login OIDC user sync with a per-subject claim
// fingerprint cache (TTL = auth.identityCacheTTL): when a subject's synced
// claims (username/email/role/department) match the last FULLY PERSISTED sync,
// the GetUser + department round-trips are skipped entirely. Only persisted
// syncs are cached — a refused cross-provider bind, a failed store write, or a
// lost create race is never cached as success, so a refused sync stays refused
// (and an unpersisted one is retried) on the next request.
type oidcSyncer struct {
	userStore       store.UserStore
	departmentClaim string
	logger          *slog.Logger
	cache           *ttlCache[string] // sub -> fingerprint of the last persisted sync
}

func newOIDCSyncer(userStore store.UserStore, departmentClaim string, ttl time.Duration, logger *slog.Logger) *oidcSyncer {
	return &oidcSyncer{
		userStore:       userStore,
		departmentClaim: departmentClaim,
		logger:          logger,
		cache:           newTTLCache[string](ttl, identityCacheMaxEntries),
	}
}

// sync provisions/re-syncs the OIDC user unless the incoming claims match the
// subject's last persisted sync. A non-nil error means the bind was refused
// and the token must be rejected — that outcome is never cached.
func (s *oidcSyncer) sync(ctx context.Context, sub, username, email, role string, claims map[string]interface{}) error {
	deptName := ""
	if s.departmentClaim != "" {
		deptName = extractStringClaim(claims, s.departmentClaim)
	}
	// %q-quote each field so the fingerprint is unambiguous regardless of
	// claim content (no separator injection).
	fp := fmt.Sprintf("%q|%q|%q|%q", username, email, role, deptName)
	if last, ok := s.cache.get(sub); ok && last == fp {
		return nil // unchanged since the last persisted sync — skip the store entirely
	}
	deptID := resolveOIDCDepartment(ctx, s.userStore, s.departmentClaim, claims, s.logger)
	persisted, err := syncOIDCUser(ctx, s.userStore, sub, username, email, role, deptID, s.logger)
	if err != nil {
		return err // NEVER cache a refused bind
	}
	if persisted {
		s.cache.put(sub, fp)
	}
	return nil
}
