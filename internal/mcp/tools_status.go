package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type getArtifactStatusInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to check"`
}

func registerStatusTool(server *mcp.Server, deploySvc *deploy.Service) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_artifact_status",
		Description: "Get detailed status information for a specific deployed artifact, including URL, runtime, and lifecycle phase.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input getArtifactStatusInput) (*mcp.CallToolResult, *api.Artifact, error) {
		if deploySvc == nil {
			return nil, nil, errDeployServiceNotConfigured
		}
		app, err := deploySvc.Get(ctx, ownerFromContext(ctx), input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		return nil, appToArtifact(app), nil
	})
}
