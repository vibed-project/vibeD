package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"

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

func registerListTool(server *mcp.Server, deploySvc *deploy.Service) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_artifacts",
		Description: "List all deployed artifacts with their status and access URLs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listArtifactsInput) (*mcp.CallToolResult, *listArtifactsOutput, error) {
		if deploySvc == nil {
			return nil, nil, errDeployServiceNotConfigured
		}

		// Fetch everything (limit 0 = all): the status filter below must apply
		// before any pagination, so server-side slicing can't be used here.
		apps, _, err := deploySvc.List(ctx, ownerFromContext(ctx), 0, 0)
		if err != nil {
			return nil, nil, err
		}
		summaries := make([]api.ArtifactSummary, 0, len(apps))
		for i := range apps {
			if input.Status != "" && input.Status != "all" &&
				string(vibedv1.StatusFromPhase(apps[i].Status.Phase)) != input.Status {
				continue
			}
			summaries = append(summaries, appToSummary(&apps[i]))
		}
		return nil, &listArtifactsOutput{
			Artifacts: summaries,
			Total:     len(summaries),
			Offset:    0,
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
