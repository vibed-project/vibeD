package mcp

import (
	"context"
	"strings"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
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

func registerLogsTool(server *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_artifact_logs",
		Description: "Retrieve recent log lines from a deployed artifact for debugging purposes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input getArtifactLogsInput) (*mcp.CallToolResult, *getArtifactLogsOutput, error) {
		lines := clampLogLines(input.Lines, limits)

		if deploySvc == nil {
			logs, err := orch.Logs(ctx, input.ArtifactID, lines)
			if err != nil {
				return nil, nil, err
			}
			return nil, &getArtifactLogsOutput{Logs: strings.Join(logs, "\n")}, nil
		}

		// VibedApp path: one bounded, non-following read of the bound pod's
		// logs, tail-seeded with the requested line count.
		var logs []string
		err := deploySvc.StreamLogs(ctx, ownerFromContext(ctx), input.ArtifactID, false, int64(lines), func(l string) error {
			logs = append(logs, l)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		return nil, &getArtifactLogsOutput{Logs: strings.Join(logs, "\n")}, nil
	})
}
