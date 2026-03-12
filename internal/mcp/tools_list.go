package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listArtifactsInput struct {
	Status string `json:"status,omitempty" jsonschema:"Filter by status: running building failed all (default: all)"`
}

type listArtifactsOutput struct {
	Artifacts []api.ArtifactSummary `json:"artifacts"`
}

func registerListTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_artifacts",
		Description: "List all deployed artifacts with their status, deployment target, and access URLs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listArtifactsInput) (*mcp.CallToolResult, *listArtifactsOutput, error) {
		artifacts, err := orch.List(ctx, input.Status)
		if err != nil {
			return nil, nil, err
		}
		return nil, &listArtifactsOutput{Artifacts: artifacts}, nil
	})
}
