package mcp

import (
	"context"
	"fmt"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"time"
)

type listUsersInput struct {
	DepartmentID string `json:"department_id,omitempty" jsonschema:"Filter users by department ID. Omit to list all users."`
}

type listUsersOutput struct {
	Users []api.User `json:"users"`
}

func registerListUsersTool(server *mcp.Server, userStore store.UserStore) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_users",
		Description: "List all vibeD users. Requires admin role. Optionally filter by department.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listUsersInput) (*mcp.CallToolResult, *listUsersOutput, error) {
		if !vibedauth.IsAdmin(ctx) {
			return nil, nil, fmt.Errorf("admin access required")
		}
		users, err := userStore.ListUsers(ctx, input.DepartmentID)
		if err != nil {
			return nil, nil, err
		}
		return nil, &listUsersOutput{Users: users}, nil
	})
}

type getUserInput struct {
	UserID string `json:"user_id" jsonschema:"ID of the user to retrieve"`
}

func registerGetUserTool(server *mcp.Server, userStore store.UserStore) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_user",
		Description: "Get details of a specific vibeD user. Admins can view any user; regular users can only view themselves.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input getUserInput) (*mcp.CallToolResult, *api.User, error) {
		callerID := vibedauth.UserIDFromContext(ctx)
		if !vibedauth.IsAdmin(ctx) && callerID != input.UserID {
			return nil, nil, fmt.Errorf("user not found")
		}
		user, err := userStore.GetUser(ctx, input.UserID)
		if err != nil {
			return nil, nil, err
		}
		return nil, user, nil
	})
}

// --- Department tools ---

type listDepartmentsInput struct{}

type listDepartmentsOutput struct {
	Departments []api.Department `json:"departments"`
}

func registerListDepartmentsTool(server *mcp.Server, userStore store.UserStore) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_departments",
		Description: "List all departments in vibeD.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listDepartmentsInput) (*mcp.CallToolResult, *listDepartmentsOutput, error) {
		depts, err := userStore.ListDepartments(ctx)
		if err != nil {
			return nil, nil, err
		}
		if depts == nil {
			depts = []api.Department{}
		}
		return nil, &listDepartmentsOutput{Departments: depts}, nil
	})
}

type createDepartmentInput struct {
	Name string `json:"name" jsonschema:"Name of the department to create"`
}

func registerCreateDepartmentTool(server *mcp.Server, userStore store.UserStore) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_department",
		Description: "Create a new department. Requires admin role.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input createDepartmentInput) (*mcp.CallToolResult, *api.Department, error) {
		if !vibedauth.IsAdmin(ctx) {
			return nil, nil, fmt.Errorf("admin access required")
		}
		if input.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}
		now := time.Now()
		dept := &api.Department{
			ID:        fmt.Sprintf("dept-%x", now.UnixNano()),
			Name:      input.Name,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := userStore.CreateDepartment(ctx, dept); err != nil {
			return nil, nil, err
		}
		return nil, dept, nil
	})
}
