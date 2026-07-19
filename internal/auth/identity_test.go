package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
)

// countingUserStore is a thread-safe store.UserStore that counts store calls,
// so tests can assert exactly how many lookups an authenticated request costs.
type countingUserStore struct {
	mu         sync.Mutex
	users      map[string]*api.User
	byHash     map[string]string // runtime API key hash -> user ID
	failUpdate bool

	getUserCalls    int
	createUserCalls int
	updateUserCalls int
}

func newCountingUserStore() *countingUserStore {
	return &countingUserStore{users: map[string]*api.User{}, byHash: map[string]string{}}
}

func (s *countingUserStore) GetUser(_ context.Context, id string) (*api.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getUserCalls++
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, errNoUser
}

func (s *countingUserStore) CreateUser(_ context.Context, u *api.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createUserCalls++
	if _, ok := s.users[u.ID]; ok {
		return fmt.Errorf("exists")
	}
	cp := *u
	s.users[u.ID] = &cp
	return nil
}

func (s *countingUserStore) UpdateUser(_ context.Context, u *api.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateUserCalls++
	if s.failUpdate {
		return fmt.Errorf("store write failed")
	}
	cp := *u
	s.users[u.ID] = &cp
	return nil
}

func (s *countingUserStore) GetUserByAPIKeyHash(_ context.Context, hash string) (*api.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byHash[hash]; ok {
		if u, ok := s.users[id]; ok {
			cp := *u
			return &cp, nil
		}
	}
	return nil, errNoUser
}

func (s *countingUserStore) GetUserByName(context.Context, string) (*api.User, error) {
	return nil, errNoUser
}
func (s *countingUserStore) ListUsers(context.Context, string, int, int) ([]api.User, error) {
	return nil, nil
}
func (s *countingUserStore) CreateDepartment(context.Context, *api.Department) error { return nil }
func (s *countingUserStore) GetDepartment(context.Context, string) (*api.Department, error) {
	return nil, errNoUser
}
func (s *countingUserStore) GetDepartmentByName(context.Context, string) (*api.Department, error) {
	return nil, errNoUser
}
func (s *countingUserStore) ListDepartments(context.Context, int, int) ([]api.Department, error) {
	return nil, nil
}
func (s *countingUserStore) UpdateDepartment(context.Context, *api.Department) error { return nil }
func (s *countingUserStore) DeleteDepartment(context.Context, string) error          { return nil }

// mutate runs fn under the store lock — for tests flipping status/role
// between requests.
func (s *countingUserStore) mutate(fn func(users map[string]*api.User)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.users)
}

func (s *countingUserStore) counts() (get, create, update int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getUserCalls, s.createUserCalls, s.updateUserCalls
}

func (s *countingUserStore) resetCounts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getUserCalls, s.createUserCalls, s.updateUserCalls = 0, 0, 0
}

// snapshot returns a copy of the stored user under the lock (nil when absent).
func (s *countingUserStore) snapshot(id string) *api.User {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp
	}
	return nil
}

// addRuntimeKeyUser stores a user authenticating via a runtime-generated API
// key (the GetUserByAPIKeyHash path), which is NOT in the static roleMap so
// its role must come from the resolved identity.
func (s *countingUserStore) addRuntimeKeyUser(id, role, token string) {
	h := sha256.Sum256([]byte(token))
	hh := hex.EncodeToString(h[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[id] = &api.User{ID: id, Name: id, Role: role, Status: "active", Provider: "local", APIKeyHash: hh}
	s.byHash[hh] = id
}

// newIdentityTestServer builds the same chain runHTTPServer wires: bearer auth
// (with identity resolution + suspended check) outside RoleMiddleware, around
// a handler that echoes the resolved role.
func newIdentityTestServer(t *testing.T, cfg config.AuthConfig, st store.UserStore) *httptest.Server {
	t.Helper()
	mw, _, err := Build(cfg, st, discardLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, RoleFromContext(r.Context()))
	})
	roleMW := RoleMiddleware(BuildRoleMap(cfg.APIKeys), st)
	return httptest.NewServer(mw(roleMW(inner)))
}

func getWithToken(t *testing.T, srvURL, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srvURL+"/api/apps", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

func apikeyCfg(ttl time.Duration) config.AuthConfig {
	return config.AuthConfig{
		Enabled: true, Mode: "apikey",
		APIKeys:          []config.APIKeyConf{{Key: "static-secret", Name: "svc"}},
		IdentityCacheTTL: ttl,
	}
}

// TestIdentityCache_SuspensionPropagation is regression (a): with the cache
// enabled a suspension takes effect within (at most) the TTL, and with the
// cache disabled (TTL=0) it takes effect immediately.
func TestIdentityCache_SuspensionPropagation(t *testing.T) {
	const key = "static-secret"

	t.Run("cached identity is served within the TTL", func(t *testing.T) {
		st := newCountingUserStore()
		srv := newIdentityTestServer(t, apikeyCfg(time.Minute), st)
		defer srv.Close()

		if code, _ := getWithToken(t, srv.URL, key); code != http.StatusOK {
			t.Fatalf("first request: got %d, want 200", code)
		}
		st.mutate(func(users map[string]*api.User) { users["apikey-svc"].Status = "suspended" })
		// Still allowed: the identity was cached before the suspension. This is
		// the documented propagation bound (= TTL).
		if code, _ := getWithToken(t, srv.URL, key); code != http.StatusOK {
			t.Errorf("within TTL: got %d, want 200 (suspension propagates only after the TTL)", code)
		}
	})

	t.Run("suspension enforced after the TTL expires", func(t *testing.T) {
		st := newCountingUserStore()
		srv := newIdentityTestServer(t, apikeyCfg(10*time.Millisecond), st)
		defer srv.Close()

		if code, _ := getWithToken(t, srv.URL, key); code != http.StatusOK {
			t.Fatalf("first request: got %d, want 200", code)
		}
		st.mutate(func(users map[string]*api.User) { users["apikey-svc"].Status = "suspended" })
		time.Sleep(50 * time.Millisecond) // > TTL: the cached entry must have expired
		if code, _ := getWithToken(t, srv.URL, key); code != http.StatusUnauthorized {
			t.Errorf("after TTL: got %d, want 401", code)
		}
	})

	t.Run("TTL=0 disables caching so suspension is immediate", func(t *testing.T) {
		st := newCountingUserStore()
		srv := newIdentityTestServer(t, apikeyCfg(0), st)
		defer srv.Close()

		if code, _ := getWithToken(t, srv.URL, key); code != http.StatusOK {
			t.Fatalf("first request: got %d, want 200", code)
		}
		st.mutate(func(users map[string]*api.User) { users["apikey-svc"].Status = "suspended" })
		if code, _ := getWithToken(t, srv.URL, key); code != http.StatusUnauthorized {
			t.Errorf("immediately after suspension: got %d, want 401", code)
		}
	})
}

// TestIdentityCache_RoleChangePropagation is regression (b): a store-side role
// change reaches RoleMiddleware within the TTL (and immediately with TTL=0).
// Uses a runtime API key, whose role is NOT in the static roleMap and so must
// flow from the resolved identity.
func TestIdentityCache_RoleChangePropagation(t *testing.T) {
	const token = "runtime-token-1"

	t.Run("TTL=0 propagates immediately", func(t *testing.T) {
		st := newCountingUserStore()
		st.addRuntimeKeyUser("u1", "user", token)
		srv := newIdentityTestServer(t, apikeyCfg(0), st)
		defer srv.Close()

		if _, body := getWithToken(t, srv.URL, token); body != "user" {
			t.Fatalf("initial role = %q, want user", body)
		}
		st.mutate(func(users map[string]*api.User) { users["u1"].Role = "admin" })
		if _, body := getWithToken(t, srv.URL, token); body != "admin" {
			t.Errorf("after role change (TTL=0): role = %q, want admin", body)
		}
	})

	t.Run("cached role is served within the TTL and refreshed after it", func(t *testing.T) {
		st := newCountingUserStore()
		st.addRuntimeKeyUser("u1", "user", token)
		srv := newIdentityTestServer(t, apikeyCfg(10*time.Millisecond), st)
		defer srv.Close()

		if _, body := getWithToken(t, srv.URL, token); body != "user" {
			t.Fatalf("initial role = %q, want user", body)
		}
		st.mutate(func(users map[string]*api.User) { users["u1"].Role = "admin" })
		time.Sleep(50 * time.Millisecond) // > TTL
		if _, body := getWithToken(t, srv.URL, token); body != "admin" {
			t.Errorf("after TTL: role = %q, want admin", body)
		}
	})

	t.Run("stale role persists on a warm cache", func(t *testing.T) {
		st := newCountingUserStore()
		st.addRuntimeKeyUser("u1", "user", token)
		srv := newIdentityTestServer(t, apikeyCfg(time.Minute), st)
		defer srv.Close()

		if _, body := getWithToken(t, srv.URL, token); body != "user" {
			t.Fatalf("initial role = %q, want user", body)
		}
		st.mutate(func(users map[string]*api.User) { users["u1"].Role = "admin" })
		if _, body := getWithToken(t, srv.URL, token); body != "user" {
			t.Errorf("within TTL: role = %q, want the cached 'user' (propagation bound = TTL)", body)
		}
	})
}

// TestOIDCSyncer_BindRefusalNeverCached is regression (c): a refused
// cross-provider bind must stay refused on every subsequent request — only
// successful (persisted) syncs may enter the fingerprint cache.
func TestOIDCSyncer_BindRefusalNeverCached(t *testing.T) {
	st := newCountingUserStore()
	st.mutate(func(users map[string]*api.User) {
		users["collide"] = &api.User{ID: "collide", Name: "svc", Provider: "local", Status: "active"}
	})
	s := newOIDCSyncer(st, "", time.Minute, discardLogger())
	ctx := context.Background()

	if err := s.sync(ctx, "collide", "attacker", "", "admin", nil); err == nil {
		t.Fatal("cross-provider bind was allowed")
	}
	get1, _, _ := st.counts()
	// Repeat request with identical claims: MUST hit the store again and MUST
	// still be refused — a refusal cached as success would grant access.
	if err := s.sync(ctx, "collide", "attacker", "", "admin", nil); err == nil {
		t.Fatal("refused bind was cached as success")
	}
	get2, _, _ := st.counts()
	if get2 != get1+1 {
		t.Errorf("repeat refused sync skipped the store: GetUser %d -> %d, want +1", get1, get2)
	}
	if u := st.snapshot("collide"); u.Provider != "local" || u.Role == "admin" {
		t.Errorf("collided account mutated: %+v", u)
	}
}

// TestOIDCSyncer_UnchangedClaimsSkipStore: a successful sync is fingerprint-
// cached, so unchanged-claims requests within the TTL skip GetUser/UpdateUser
// entirely; changed claims trigger a real re-sync.
func TestOIDCSyncer_UnchangedClaimsSkipStore(t *testing.T) {
	st := newCountingUserStore()
	s := newOIDCSyncer(st, "", time.Minute, discardLogger())
	ctx := context.Background()

	if err := s.sync(ctx, "sub-1", "alice", "a@corp", "user", nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	get1, create1, _ := st.counts()
	if create1 != 1 {
		t.Fatalf("CreateUser calls = %d, want 1", create1)
	}
	// Same claims again: fully served from the fingerprint cache.
	for i := 0; i < 3; i++ {
		if err := s.sync(ctx, "sub-1", "alice", "a@corp", "user", nil); err != nil {
			t.Fatalf("repeat sync: %v", err)
		}
	}
	if get2, _, _ := st.counts(); get2 != get1 {
		t.Errorf("unchanged claims hit the store: GetUser %d -> %d, want no change", get1, get2)
	}
	// Changed claims (role promotion): must re-sync and persist.
	if err := s.sync(ctx, "sub-1", "alice", "a@corp", "admin", nil); err != nil {
		t.Fatalf("changed-claims sync: %v", err)
	}
	if get3, _, update3 := st.counts(); get3 != get1+1 || update3 != 1 {
		t.Errorf("changed claims: GetUser=%d (want %d), UpdateUser=%d (want 1)", get3, get1+1, update3)
	}
	if u := st.snapshot("sub-1"); u.Role != "admin" {
		t.Errorf("role = %q, want admin", u.Role)
	}
}

// TestOIDCSyncer_FailedWriteNotCached: a sync whose UpdateUser failed is not
// fingerprint-cached, so the next request retries instead of silently serving
// an unsynced record for a full TTL.
func TestOIDCSyncer_FailedWriteNotCached(t *testing.T) {
	st := newCountingUserStore()
	st.mutate(func(users map[string]*api.User) {
		users["sub-2"] = &api.User{ID: "sub-2", Name: "bob", Provider: "oidc", Role: "user", Status: "active"}
	})
	st.failUpdate = true
	s := newOIDCSyncer(st, "", time.Minute, discardLogger())
	ctx := context.Background()

	if err := s.sync(ctx, "sub-2", "bob", "", "admin", nil); err != nil {
		t.Fatalf("sync with failing write should not reject the token: %v", err)
	}
	get1, _, _ := st.counts()
	if err := s.sync(ctx, "sub-2", "bob", "", "admin", nil); err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	if get2, _, _ := st.counts(); get2 != get1+1 {
		t.Errorf("failed write was fingerprint-cached: GetUser %d -> %d, want +1 (retry)", get1, get2)
	}
}

// TestAPIKeyAutoProvision_ExactlyOnce is regression (d): the static-key user is
// auto-provisioned exactly once no matter how many requests authenticate, and
// later requests don't repeat the provisioning existence check.
func TestAPIKeyAutoProvision_ExactlyOnce(t *testing.T) {
	st := newCountingUserStore()
	srv := newIdentityTestServer(t, apikeyCfg(0), st)
	defer srv.Close()

	for i := 0; i < 5; i++ {
		if code, _ := getWithToken(t, srv.URL, "static-secret"); code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i, code)
		}
	}
	_, create, _ := st.counts()
	if create != 1 {
		t.Errorf("CreateUser calls = %d, want exactly 1", create)
	}
	u := st.snapshot("apikey-svc")
	if u == nil || u.Status != "active" || u.Provider != "local" {
		t.Errorf("provisioned user = %+v", u)
	}
}

// TestIdentityResolution_StoreCallCounting is regression (e): with caching
// disabled an authenticated request costs exactly one GetUser, and N requests
// within the TTL cost one in total.
func TestIdentityResolution_StoreCallCounting(t *testing.T) {
	const token = "runtime-token-2"

	t.Run("TTL=0: exactly one GetUser per request", func(t *testing.T) {
		st := newCountingUserStore()
		st.addRuntimeKeyUser("u2", "user", token)
		srv := newIdentityTestServer(t, apikeyCfg(0), st)
		defer srv.Close()

		for i := 1; i <= 3; i++ {
			if code, _ := getWithToken(t, srv.URL, token); code != http.StatusOK {
				t.Fatalf("request %d: got %d, want 200", i, code)
			}
			if get, _, _ := st.counts(); get != i {
				t.Errorf("after request %d: GetUser calls = %d, want exactly %d (1 per request)", i, get, i)
			}
		}
	})

	t.Run("TTL>0: N requests within the TTL cost one GetUser", func(t *testing.T) {
		st := newCountingUserStore()
		st.addRuntimeKeyUser("u2", "user", token)
		srv := newIdentityTestServer(t, apikeyCfg(time.Minute), st)
		defer srv.Close()

		for i := 1; i <= 5; i++ {
			if code, _ := getWithToken(t, srv.URL, token); code != http.StatusOK {
				t.Fatalf("request %d: got %d, want 200", i, code)
			}
		}
		if get, _, _ := st.counts(); get != 1 {
			t.Errorf("GetUser calls = %d, want exactly 1 for 5 requests within the TTL", get)
		}
	})

	t.Run("static key steady state: one GetUser per request", func(t *testing.T) {
		st := newCountingUserStore()
		srv := newIdentityTestServer(t, apikeyCfg(0), st)
		defer srv.Close()

		// First request pays the one-time provisioning existence check on top of
		// the identity resolution.
		if code, _ := getWithToken(t, srv.URL, "static-secret"); code != http.StatusOK {
			t.Fatalf("warm-up request: got %d, want 200", code)
		}
		st.resetCounts()
		for i := 1; i <= 3; i++ {
			if code, _ := getWithToken(t, srv.URL, "static-secret"); code != http.StatusOK {
				t.Fatalf("request %d: got %d, want 200", i, code)
			}
			if get, _, _ := st.counts(); get != i {
				t.Errorf("after request %d: GetUser calls = %d, want exactly %d", i, get, i)
			}
		}
	})
}

// TestRoleMiddleware_StoreFallbackWithoutIdentity: when no request-scoped
// identity was resolved (an exotic middleware order that bypasses Build), the
// middleware still falls back to its pre-identity store lookup.
func TestRoleMiddleware_StoreFallbackWithoutIdentity(t *testing.T) {
	st := newCountingUserStore()
	st.mutate(func(users map[string]*api.User) {
		users["u3"] = &api.User{ID: "u3", Role: "admin", Status: "active"}
	})
	var got string
	h := RoleMiddleware(nil, st)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = RoleFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req = req.WithContext(WithUserID(req.Context(), "u3"))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "admin" {
		t.Errorf("role = %q, want admin (store fallback)", got)
	}
	if get, _, _ := st.counts(); get != 1 {
		t.Errorf("GetUser calls = %d, want 1 (fallback lookup)", get)
	}
}

// TestRoleMiddleware_PrefersContextIdentity: with a resolved identity in
// context, RoleMiddleware must not query the store again.
func TestRoleMiddleware_PrefersContextIdentity(t *testing.T) {
	st := newCountingUserStore()
	var got string
	h := RoleMiddleware(nil, st)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = RoleFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	ctx := WithUserID(req.Context(), "u4")
	ctx = WithIdentity(ctx, &Identity{ID: "u4", Role: "admin", Status: "active"})
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
	if got != "admin" {
		t.Errorf("role = %q, want admin (from context identity)", got)
	}
	if get, _, _ := st.counts(); get != 0 {
		t.Errorf("GetUser calls = %d, want 0 (identity already resolved)", get)
	}
}

// TestTTLCache_BoundAndExpiry: the cache honors its size bound (sampled
// eviction) and expires entries after the TTL; ttl<=0 disables it.
func TestTTLCache_BoundAndExpiry(t *testing.T) {
	c := newTTLCache[string](time.Minute, 3)
	for i := 0; i < 5; i++ {
		c.put(fmt.Sprintf("k%d", i), "v")
	}
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	if n > 3 {
		t.Errorf("cache grew to %d entries, bound is 3", n)
	}

	exp := newTTLCache[string](5*time.Millisecond, 10)
	exp.put("k", "v")
	if _, ok := exp.get("k"); !ok {
		t.Error("fresh entry missing")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := exp.get("k"); ok {
		t.Error("expired entry still served")
	}

	off := newTTLCache[string](0, 10)
	off.put("k", "v")
	if _, ok := off.get("k"); ok {
		t.Error("ttl=0 cache returned a hit; caching must be disabled")
	}
}
