package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/pkg/api"
)

// fakeUserStore is a minimal store.UserStore for auth tests: only user CRUD is
// meaningful; the department methods satisfy the interface.
type fakeUserStore struct{ users map[string]*api.User }

func newFakeUserStore() *fakeUserStore { return &fakeUserStore{users: map[string]*api.User{}} }

var errNoUser = errors.New("user not found")

func (f *fakeUserStore) GetUser(_ context.Context, id string) (*api.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, errNoUser
}
func (f *fakeUserStore) CreateUser(_ context.Context, u *api.User) error {
	f.users[u.ID] = u
	return nil
}
func (f *fakeUserStore) UpdateUser(_ context.Context, u *api.User) error {
	f.users[u.ID] = u
	return nil
}
func (f *fakeUserStore) GetUserByName(context.Context, string) (*api.User, error) {
	return nil, errNoUser
}
func (f *fakeUserStore) ListUsers(context.Context, string, int, int) ([]api.User, error) {
	return nil, nil
}
func (f *fakeUserStore) GetUserByAPIKeyHash(context.Context, string) (*api.User, error) {
	return nil, errNoUser
}
func (f *fakeUserStore) CreateDepartment(context.Context, *api.Department) error { return nil }
func (f *fakeUserStore) GetDepartment(context.Context, string) (*api.Department, error) {
	return nil, errNoUser
}
func (f *fakeUserStore) GetDepartmentByName(context.Context, string) (*api.Department, error) {
	return nil, errNoUser
}
func (f *fakeUserStore) ListDepartments(context.Context, int, int) ([]api.Department, error) {
	return nil, nil
}
func (f *fakeUserStore) UpdateDepartment(context.Context, *api.Department) error   { return nil }
func (f *fakeUserStore) DeleteDepartment(context.Context, string) error            { return nil }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestApiKeyUserID_Canonical: the role map keys on the same canonical ID the
// verifier emits, so RoleMiddleware's lookup can't miss.
func TestApiKeyUserID_Canonical(t *testing.T) {
	if got := apiKeyUserID("contractor"); got != "apikey-contractor" {
		t.Errorf("apiKeyUserID(contractor) = %q, want apikey-contractor", got)
	}
	if got := apiKeyUserID(""); got != "" {
		t.Errorf("apiKeyUserID(empty) = %q, want empty (no identity)", got)
	}
	rm := BuildRoleMap([]config.APIKeyConf{{Name: "contractor", Role: "admin"}})
	if rm["apikey-contractor"] != "admin" {
		t.Errorf("role map = %v, want key apikey-contractor -> admin", rm)
	}
}

// TestSuspendedStaticKeyUser_Denied is the #48 regression guard: a suspended
// static-key user must get 401. Before the canonical-ID fix, the suspended
// record (stored as apikey-<name>) was never found by the check (which looked
// up key.Name), so a suspended contractor kept full access.
func TestSuspendedStaticKeyUser_Denied(t *testing.T) {
	const key = "s3cr3t"
	cfg := config.AuthConfig{
		Enabled: true, Mode: "apikey",
		APIKeys: []config.APIKeyConf{{Key: key, Name: "contractor"}},
	}

	serve := func(store *fakeUserStore) *httptest.Server {
		mw, _, err := Build(cfg, store, discardLogger())
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))
	}
	call := func(srv *httptest.Server) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/artifacts", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	suspended := newFakeUserStore()
	suspended.users["apikey-contractor"] = &api.User{ID: "apikey-contractor", Name: "contractor", Status: "suspended"}
	sSrv := serve(suspended)
	defer sSrv.Close()
	if code := call(sSrv); code != http.StatusUnauthorized {
		t.Errorf("suspended static-key user: got %d, want 401", code)
	}

	// Control: an active user (auto-provisioned on first auth) is allowed.
	active := serve(newFakeUserStore())
	defer active.Close()
	if code := call(active); code != http.StatusOK {
		t.Errorf("active static-key user: got %d, want 200", code)
	}
}
