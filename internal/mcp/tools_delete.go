package mcp

import (
	"context"
	"fmt"

	"github.com/vibed-project/vibeD/internal/deploy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type deleteArtifactInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to delete"`
}

type deleteArtifactResult struct {
	Message string `json:"message"`
}

func registerDeleteTool(server *mcp.Server, deploySvc *deploy.Service) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_artifact",
		Description: "Stop and remove a deployed artifact. This deletes the deployment, stored source, and all associated resources.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input deleteArtifactInput) (*mcp.CallToolResult, *deleteArtifactResult, error) {
		if deploySvc == nil {
			return nil, nil, errDeployServiceNotConfigured
		}
		if err := deploySvc.Delete(ctx, ownerFromContext(ctx), input.ArtifactID); err != nil {
			return nil, nil, err
		}
		return nil, &deleteArtifactResult{
			Message: fmt.Sprintf("Artifact %q deleted successfully.", input.ArtifactID),
		}, nil
	})
}
