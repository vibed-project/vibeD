package mcp

import (
	"context"
	"fmt"

	"github.com/vibed-project/vibeD/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type deleteArtifactInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to delete"`
}

type deleteArtifactOutput struct {
	Message string `json:"message"`
}

func registerDeleteTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_artifact",
		Description: "Stop and remove a deployed artifact. This deletes the deployment, stored source code, and all associated resources.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input deleteArtifactInput) (*mcp.CallToolResult, *deleteArtifactOutput, error) {
		err := orch.Delete(ctx, input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		return nil, &deleteArtifactOutput{
			Message: fmt.Sprintf("Artifact %q deleted successfully.", input.ArtifactID),
		}, nil
	})
}
