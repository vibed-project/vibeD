package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type updateArtifactInput struct {
	ArtifactID string            `json:"artifact_id" jsonschema:"ID of the artifact to update"`
	Files      map[string]string `json:"files" jsonschema:"Updated file map (full replacement of source files)"`
	EnvVars    map[string]string `json:"env_vars,omitempty" jsonschema:"Updated environment variables"`
	SecretRefs map[string]string `json:"secret_refs,omitempty" jsonschema:"Updated secret references in format 'secret-name:key'"`
}

func registerUpdateTool(server *mcp.Server, orch *orchestrator.Orchestrator, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_artifact",
		Description: "Update an existing deployed artifact with new source files. Triggers a rebuild and redeployment.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input updateArtifactInput) (*mcp.CallToolResult, *orchestrator.DeployResult, error) {
		if err := validateFileLimits(input.Files, limits); err != nil {
			return nil, nil, err
		}

		result, err := orch.Update(ctx, orchestrator.UpdateRequest{
			ArtifactID: input.ArtifactID,
			Files:      input.Files,
			EnvVars:    input.EnvVars,
			SecretRefs: input.SecretRefs,
		})
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
