package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type getArtifactStatusInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to check"`
}

func registerStatusTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_artifact_status",
		Description: "Get detailed status information for a specific deployed artifact, including URL, deployment target, image reference, and any errors.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input getArtifactStatusInput) (*mcp.CallToolResult, *api.Artifact, error) {
		artifact, err := orch.Status(ctx, input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		return nil, artifact, nil
	})
}
