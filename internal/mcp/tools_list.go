package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listArtifactsInput struct {
	Status string `json:"status,omitempty" jsonschema:"Filter by status: running building failed all (default: all)"`
	Offset int    `json:"offset,omitempty" jsonschema:"Number of artifacts to skip (default 0)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Max artifacts to return (default 50, max 200)"`
}

type listArtifactsOutput struct {
	Artifacts []api.ArtifactSummary `json:"artifacts"`
	Total     int                   `json:"total"`
	Offset    int                   `json:"offset"`
	Limit     int                   `json:"limit"`
}

func registerListTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_artifacts",
		Description: "List all deployed artifacts with their status, deployment target, and access URLs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listArtifactsInput) (*mcp.CallToolResult, *listArtifactsOutput, error) {
		result, err := orch.List(ctx, input.Status, input.Offset, input.Limit)
		if err != nil {
			return nil, nil, err
		}
		return nil, &listArtifactsOutput{
			Artifacts: result.Artifacts,
			Total:     result.Total,
			Offset:    input.Offset,
			Limit:     clampLimit(input.Limit),
		}, nil
	})
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}
