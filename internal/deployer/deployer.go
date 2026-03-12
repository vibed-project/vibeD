package deployer

import (
	"context"

	"github.com/vibed-project/vibeD/pkg/api"
)

// DeployResult contains the outcome of a deployment.
type DeployResult struct {
	URL string
}

// Deployer deploys artifacts to a specific backend.
type Deployer interface {
	// Deploy creates a new deployment for the artifact.
	Deploy(ctx context.Context, artifact *api.Artifact) (*DeployResult, error)

	// Update updates an existing deployment with a new image or config.
	Update(ctx context.Context, artifact *api.Artifact) (*DeployResult, error)

	// Delete removes the deployment.
	Delete(ctx context.Context, artifact *api.Artifact) error

	// GetURL returns the current access URL for a deployed artifact.
	GetURL(ctx context.Context, artifact *api.Artifact) (string, error)

	// GetLogs returns recent log lines from the deployed artifact.
	GetLogs(ctx context.Context, artifact *api.Artifact, lines int) ([]string, error)
}
