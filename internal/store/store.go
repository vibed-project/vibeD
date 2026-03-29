package store

import (
	"context"

	"github.com/vibed-project/vibeD/pkg/api"
)

// UserStore persists user identities and departments.
type UserStore interface {
	CreateUser(ctx context.Context, user *api.User) error
	GetUser(ctx context.Context, id string) (*api.User, error)
	GetUserByName(ctx context.Context, name string) (*api.User, error)
	ListUsers(ctx context.Context, departmentID string) ([]api.User, error)
	GetUserByAPIKeyHash(ctx context.Context, hash string) (*api.User, error)
	UpdateUser(ctx context.Context, user *api.User) error

	CreateDepartment(ctx context.Context, dept *api.Department) error
	GetDepartment(ctx context.Context, id string) (*api.Department, error)
	GetDepartmentByName(ctx context.Context, name string) (*api.Department, error)
	ListDepartments(ctx context.Context) ([]api.Department, error)
	UpdateDepartment(ctx context.Context, dept *api.Department) error
	DeleteDepartment(ctx context.Context, id string) error
}

// ShareLinkStore persists public share links.
type ShareLinkStore interface {
	CreateShareLink(ctx context.Context, link *api.ShareLink, passwordHash string) error
	GetShareLink(ctx context.Context, token string) (*api.ShareLink, string, error) // returns link + password hash
	ListShareLinks(ctx context.Context, artifactID string) ([]api.ShareLink, error)
	RevokeShareLink(ctx context.Context, token string) error
}

// ListOptions configures artifact list queries.
type ListOptions struct {
	StatusFilter string
	OwnerID      string
	AdminView    bool
	Offset       int
	Limit        int // 0 means no limit (return all)
}

// ListResult contains paginated artifact results.
type ListResult struct {
	Artifacts []api.ArtifactSummary `json:"artifacts"`
	Total     int                   `json:"total"`
}

// ArtifactStore persists artifact metadata and state.
type ArtifactStore interface {
	// Create stores a new artifact. Returns ErrAlreadyExists if name is taken.
	Create(ctx context.Context, artifact *api.Artifact) error

	// Get retrieves an artifact by ID. Returns ErrNotFound if not found.
	Get(ctx context.Context, id string) (*api.Artifact, error)

	// GetByName retrieves an artifact by name. Returns ErrNotFound if not found.
	GetByName(ctx context.Context, name string) (*api.Artifact, error)

	// List returns artifacts matching the options with pagination.
	List(ctx context.Context, opts ListOptions) (*ListResult, error)

	// Update replaces the artifact record. Returns ErrNotFound if not found.
	Update(ctx context.Context, artifact *api.Artifact) error

	// Delete removes an artifact and its version history by ID. Returns ErrNotFound if not found.
	Delete(ctx context.Context, id string) error

	// CreateVersion stores a version snapshot for an artifact.
	CreateVersion(ctx context.Context, version *api.ArtifactVersion) error

	// ListVersions returns all version snapshots for an artifact, ordered by version number.
	ListVersions(ctx context.Context, artifactID string) ([]api.ArtifactVersion, error)

	// GetVersion retrieves a specific version snapshot. Returns ErrVersionNotFound if not found.
	GetVersion(ctx context.Context, artifactID string, version int) (*api.ArtifactVersion, error)
}
