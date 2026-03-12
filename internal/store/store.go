package store

import (
	"context"

	"github.com/vibed-project/vibeD/pkg/api"
)

// ArtifactStore persists artifact metadata and state.
type ArtifactStore interface {
	// Create stores a new artifact. Returns ErrAlreadyExists if name is taken.
	Create(ctx context.Context, artifact *api.Artifact) error

	// Get retrieves an artifact by ID. Returns ErrNotFound if not found.
	Get(ctx context.Context, id string) (*api.Artifact, error)

	// GetByName retrieves an artifact by name. Returns ErrNotFound if not found.
	GetByName(ctx context.Context, name string) (*api.Artifact, error)

	// List returns all artifacts, optionally filtered by status and owner.
	// When ownerID is non-empty, only artifacts owned by or shared with that user are returned.
	// When ownerID is empty or adminView is true, all artifacts are returned.
	List(ctx context.Context, statusFilter string, ownerID string, adminView bool) ([]api.ArtifactSummary, error)

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
