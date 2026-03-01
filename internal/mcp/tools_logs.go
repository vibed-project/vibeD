package mcp

import (
	"context"
	"strings"

	"github.com/maxkorbacher/vibed/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type getArtifactLogsInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact"`
	Lines      int    `json:"lines,omitempty" jsonschema:"Number of log lines to return (default: 50)"`
}

type getArtifactLogsOutput struct {
	Logs string `json:"logs"`
}

func registerLogsTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_artifact_logs",
		Description: "Retrieve recent log lines from a deployed artifact for debugging purposes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input getArtifactLogsInput) (*mcp.CallToolResult, *getArtifactLogsOutput, error) {
		lines := input.Lines
		if lines <= 0 {
			lines = 50
		}

		logs, err := orch.Logs(ctx, input.ArtifactID, lines)
		if err != nil {
			return nil, nil, err
		}

		return nil, &getArtifactLogsOutput{Logs: strings.Join(logs, "\n")}, nil
	})
}
