package auth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/vibed-project/vibeD/pkg/api"
)

// oidcFakeStore is a minimal UserStore for the OIDC provisioning/re-sync tests.
type oidcFakeStore struct {
	users map[string]*api.User
	depts map[string]*api.Department // by name
}

func newOIDCFakeStore() *oidcFakeStore {
	return &oidcFakeStore{users: map[string]*api.User{}, depts: map[string]*api.Department{}}
}

func (f *oidcFakeStore) GetUser(_ context.Context, id string) (*api.User, error) {
	if u, ok := f.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, fmt.Errorf("not found")
}
func (f *oidcFakeStore) CreateUser(_ context.Context, u *api.User) error {
	if _, ok := f.users[u.ID]; ok {
		return fmt.Errorf("exists")
	}
	cp := *u
	f.users[u.ID] = &cp
	return nil
}
func (f *oidcFakeStore) UpdateUser(_ context.Context, u *api.User) error {
	cp := *u
	f.users[u.ID] = &cp
	return nil
}
func (f *oidcFakeStore) CreateDepartment(_ context.Context, d *api.Department) error {
	if _, ok := f.depts[d.Name]; ok {
		return fmt.Errorf("exists")
	}
	cp := *d
	f.depts[d.Name] = &cp
	return nil
}
func (f *oidcFakeStore) GetDepartmentByName(_ context.Context, name string) (*api.Department, error) {
	if d, ok := f.depts[name]; ok {
		cp := *d
		return &cp, nil
	}
	return nil, fmt.Errorf("not found")
}

// Unused UserStore methods.
func (f *oidcFakeStore) GetUserByName(context.Context, string) (*api.User, error) {
	return nil, fmt.Errorf("n/a")
}
func (f *oidcFakeStore) GetUserByAPIKeyHash(context.Context, string) (*api.User, error) {
	return nil, fmt.Errorf("n/a")
}
func (f *oidcFakeStore) ListUsers(context.Context, string, int, int) ([]api.User, error) {
	return nil, nil
}
func (f *oidcFakeStore) GetDepartment(context.Context, string) (*api.Department, error) {
	return nil, fmt.Errorf("n/a")
}
func (f *oidcFakeStore) ListDepartments(context.Context, int, int) ([]api.Department, error) {
	return nil, nil
}
func (f *oidcFakeStore) UpdateDepartment(context.Context, *api.Department) error { return nil }
func (f *oidcFakeStore) DeleteDepartment(context.Context, string) error          { return nil }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSyncOIDCUser_CreateAndResync(t *testing.T) {
	fs := newOIDCFakeStore()
	ctx := context.Background()
	lg := quietLogger()

	// First login: creates the user.
	if err := syncOIDCUser(ctx, fs, "sub-1", "alice", "alice@corp", "user", "dept-eng", lg); err != nil {
		t.Fatalf("create: %v", err)
	}
	u := fs.users["sub-1"]
	if u == nil || u.Provider != "oidc" || u.Role != "user" || u.DepartmentID != "dept-eng" {
		t.Fatalf("created = %+v", u)
	}
	// Second login with promoted role + new email + dept: re-syncs.
	if err := syncOIDCUser(ctx, fs, "sub-1", "alice", "alice@newcorp", "admin", "dept-plat", lg); err != nil {
		t.Fatalf("resync: %v", err)
	}
	u = fs.users["sub-1"]
	if u.Role != "admin" || u.Email != "alice@newcorp" || u.DepartmentID != "dept-plat" {
		t.Errorf("after resync = %+v, want admin/newcorp/plat", u)
	}
}

func TestSyncOIDCUser_CrossProviderBindRefused(t *testing.T) {
	fs := newOIDCFakeStore()
	ctx := context.Background()
	// An apikey account whose id collides with an OIDC sub.
	fs.users["collide"] = &api.User{ID: "collide", Name: "svc", Provider: "local", Status: "active"}

	err := syncOIDCUser(ctx, fs, "collide", "attacker", "", "admin", "", quietLogger())
	if err == nil {
		t.Fatal("cross-provider bind was allowed")
	}
	// The pre-existing account is untouched.
	if fs.users["collide"].Provider != "local" || fs.users["collide"].Role == "admin" {
		t.Errorf("collided account mutated: %+v", fs.users["collide"])
	}
}

func TestResolveOIDCDepartment_Deterministic(t *testing.T) {
	fs := newOIDCFakeStore()
	ctx := context.Background()
	claims := map[string]interface{}{"dept": "Platform Eng"}

	id1 := resolveOIDCDepartment(ctx, fs, "dept", claims, quietLogger())
	id2 := resolveOIDCDepartment(ctx, fs, "dept", claims, quietLogger())
	if id1 == "" || id1 != id2 {
		t.Fatalf("non-deterministic dept id: %q vs %q", id1, id2)
	}
	if id1 != "dept-platform-eng" {
		t.Errorf("dept id = %q, want dept-platform-eng", id1)
	}
	// Only one department row created despite two resolves.
	if len(fs.depts) != 1 {
		t.Errorf("created %d departments, want 1", len(fs.depts))
	}
	// Empty claim → no department.
	if id := resolveOIDCDepartment(ctx, fs, "", claims, quietLogger()); id != "" {
		t.Errorf("empty claim returned %q", id)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Platform Eng":  "platform-eng",
		"  R&D / Core ": "r-d-core",
		"ALL":           "all",
		"":              "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
