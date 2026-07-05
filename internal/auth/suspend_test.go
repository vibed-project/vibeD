package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"
)

// fakeUserStore is a minimal in-memory store.UserStore for auth tests. Only the
// user-record methods the suspended-user check touches are meaningful; the rest
// satisfy the interface.
type fakeUserStore struct {
	byID   map[string]*api.User
	byName map[string]*api.User
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{byID: map[string]*api.User{}, byName: map[string]*api.User{}}
}

func (s *fakeUserStore) CreateUser(_ context.Context, u *api.User) error {
	s.byID[u.ID] = u
	if u.Name != "" {
		s.byName[u.Name] = u
	}
	return nil
}

func (s *fakeUserStore) GetUser(_ context.Context, id string) (*api.User, error) {
	if u, ok := s.byID[id]; ok {
		return u, nil
	}
	return nil, &api.ErrNotFound{}
}

func (s *fakeUserStore) GetUserByName(_ context.Context, name string) (*api.User, error) {
	if u, ok := s.byName[name]; ok {
		return u, nil
	}
	return nil, &api.ErrNotFound{}
}

func (s *fakeUserStore) ListUsers(_ context.Context, _ string) ([]api.User, error) { return nil, nil }
func (s *fakeUserStore) GetUserByAPIKeyHash(_ context.Context, _ string) (*api.User, error) {
	return nil, &api.ErrNotFound{}
}
func (s *fakeUserStore) UpdateUser(_ context.Context, u *api.User) error {
	s.byID[u.ID] = u
	return nil
}
func (s *fakeUserStore) CreateDepartment(_ context.Context, _ *api.Department) error { return nil }
func (s *fakeUserStore) GetDepartment(_ context.Context, _ string) (*api.Department, error) {
	return nil, &api.ErrNotFound{}
}
func (s *fakeUserStore) GetDepartmentByName(_ context.Context, _ string) (*api.Department, error) {
	return nil, &api.ErrNotFound{}
}
func (s *fakeUserStore) ListDepartments(_ context.Context) ([]api.Department, error) { return nil, nil }
func (s *fakeUserStore) UpdateDepartment(_ context.Context, _ *api.Department) error { return nil }
func (s *fakeUserStore) DeleteDepartment(_ context.Context, _ string) error          { return nil }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestAPIKeyUserIDCanonical locks in the invariant that the verifier's UserID,
// the provisioned record ID, and the role-map key all agree on "apikey-<name>".
func TestAPIKeyUserIDCanonical(t *testing.T) {
	if got := APIKeyUserID("alice"); got != "apikey-alice" {
		t.Errorf("APIKeyUserID(alice) = %q, want apikey-alice", got)
	}
	if got := APIKeyUserID(""); got != "" {
		t.Errorf("APIKeyUserID(\"\") = %q, want empty", got)
	}

	keys := []config.APIKeyConf{{Key: "k", Name: "alice", Role: "admin"}}
	rm := BuildRoleMap(keys)
	if rm["apikey-alice"] != "admin" {
		t.Errorf("BuildRoleMap keyed on %v, want role under apikey-alice", rm)
	}
	if _, keyedByBareName := rm["alice"]; keyedByBareName {
		t.Error("BuildRoleMap must not key on the bare name (would miss the verifier's UserID)")
	}
}

// TestAPIKeyVerifierEmitsCanonicalID proves the verifier's TokenInfo.UserID is
// the same "apikey-<name>" the store record uses — the root cause of #48.
func TestAPIKeyVerifierEmitsCanonicalID(t *testing.T) {
	us := newFakeUserStore()
	keys := []config.APIKeyConf{{Key: "secret", Name: "alice"}}
	verifier := apiKeyVerifier(keys, us, discardLogger())

	req := httptest.NewRequest("GET", "/api/artifacts", nil)
	info, err := verifier(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if info.UserID != "apikey-alice" {
		t.Errorf("TokenInfo.UserID = %q, want apikey-alice", info.UserID)
	}
	// Auto-provision stored the record under the SAME id.
	if _, err := us.GetUser(context.Background(), info.UserID); err != nil {
		t.Errorf("provisioned record not found under verifier UserID %q: %v", info.UserID, err)
	}
}

// TestSuspendedStaticKeyUserBlocked is the end-to-end proof of #48: a suspended
// static-API-key user is actually rejected (401), while an active user passes.
func TestSuspendedStaticKeyUserBlocked(t *testing.T) {
	logger := discardLogger()

	newHandler := func(us *fakeUserStore) http.Handler {
		cfg := config.AuthConfig{
			Enabled: true,
			Mode:    "apikey",
			APIKeys: []config.APIKeyConf{
				{Key: "alice-key", Name: "alice"},
				{Key: "bob-key", Name: "bob"},
			},
		}
		mw, _, err := Build(cfg, us, logger)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		return SkipAuthPaths(mw)(final)
	}

	do := func(h http.Handler, token string) int {
		srv := httptest.NewServer(h)
		defer srv.Close()
		req, _ := http.NewRequest("GET", srv.URL+"/api/artifacts", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// alice authenticates once (auto-provisions her record), then is suspended.
	us := newFakeUserStore()
	h := newHandler(us)

	if code := do(h, "alice-key"); code != http.StatusOK {
		t.Fatalf("active alice: got %d, want 200", code)
	}

	// Admin suspends alice — operating on the canonical record ID.
	alice, err := us.GetUser(context.Background(), APIKeyUserID("alice"))
	if err != nil {
		t.Fatalf("alice record missing after auth: %v", err)
	}
	alice.Status = "suspended"
	_ = us.UpdateUser(context.Background(), alice)

	// The suspended user must now be blocked (this failed open before the fix).
	if code := do(h, "alice-key"); code != http.StatusUnauthorized {
		t.Errorf("suspended alice: got %d, want 401", code)
	}
	// An active user with a different key is unaffected.
	if code := do(h, "bob-key"); code != http.StatusOK {
		t.Errorf("active bob: got %d, want 200", code)
	}
}
