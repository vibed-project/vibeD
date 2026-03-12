package mcp

import (
	"context"
	"strings"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type getArtifactLogsInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact"`
	Lines      int    `json:"lines,omitempty" jsonschema:"Number of log lines to return (default: 50)"`
}

type getArtifactLogsOutput struct {
	Logs string `json:"logs"`
}

func registerLogsTool(server *mcp.Server, orch *orchestrator.Orchestrator, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_artifact_logs",
		Description: "Retrieve recent log lines from a deployed artifact for debugging purposes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input getArtifactLogsInput) (*mcp.CallToolResult, *getArtifactLogsOutput, error) {
		lines := clampLogLines(input.Lines, limits)

		logs, err := orch.Logs(ctx, input.ArtifactID, lines)
		if err != nil {
			return nil, nil, err
		}

		return nil, &getArtifactLogsOutput{Logs: strings.Join(logs, "\n")}, nil
	})
}
