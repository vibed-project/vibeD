package mcp

import (
	"context"
	"testing"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/pkg/api"
)

// deptStore is a minimal store.UserStore stub exposing just ListDepartments.
type deptStore struct {
	depts []api.Department
}

func (s *deptStore) ListDepartments(_ context.Context) ([]api.Department, error) { return s.depts, nil }

// The remaining UserStore methods are unused by listDepartments; panic if hit.
func (s *deptStore) CreateUser(context.Context, *api.User) error        { panic("unused") }
func (s *deptStore) GetUser(context.Context, string) (*api.User, error) { panic("unused") }
func (s *deptStore) GetUserByName(context.Context, string) (*api.User, error) {
	panic("unused")
}
func (s *deptStore) ListUsers(context.Context, string) ([]api.User, error) { panic("unused") }
func (s *deptStore) GetUserByAPIKeyHash(context.Context, string) (*api.User, error) {
	panic("unused")
}
func (s *deptStore) UpdateUser(context.Context, *api.User) error             { panic("unused") }
func (s *deptStore) CreateDepartment(context.Context, *api.Department) error { panic("unused") }
func (s *deptStore) GetDepartment(context.Context, string) (*api.Department, error) {
	panic("unused")
}
func (s *deptStore) GetDepartmentByName(context.Context, string) (*api.Department, error) {
	panic("unused")
}
func (s *deptStore) UpdateDepartment(context.Context, *api.Department) error { panic("unused") }
func (s *deptStore) DeleteDepartment(context.Context, string) error          { panic("unused") }

// TestListDepartmentsRequiresAdmin is the proof for #53: a non-admin caller is
// rejected, while an admin gets the list.
func TestListDepartmentsRequiresAdmin(t *testing.T) {
	store := &deptStore{depts: []api.Department{{ID: "d1", Name: "eng"}}}

	// Non-admin (regular user): denied.
	userCtx := vibedauth.WithRole(vibedauth.WithUserID(context.Background(), "apikey-bob"), "user")
	if _, err := listDepartments(userCtx, store); err == nil {
		t.Error("non-admin must be denied list_departments")
	}

	// Anonymous (no role → defaults to "user"): denied.
	if _, err := listDepartments(context.Background(), store); err == nil {
		t.Error("anonymous caller must be denied list_departments")
	}

	// Admin: allowed, returns the list.
	adminCtx := vibedauth.WithRole(context.Background(), "admin")
	out, err := listDepartments(adminCtx, store)
	if err != nil {
		t.Fatalf("admin denied: %v", err)
	}
	if len(out.Departments) != 1 || out.Departments[0].ID != "d1" {
		t.Errorf("admin got %+v, want the one department", out.Departments)
	}
}
