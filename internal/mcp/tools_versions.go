package mcp

import (
	"context"
	"fmt"

	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listVersionsInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to list versions for"`
}

type listVersionsOutput struct {
	ArtifactID string                `json:"artifact_id"`
	Versions   []api.ArtifactVersion `json:"versions"`
}

func registerListVersionsTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_versions",
		Description: "List all version snapshots for a deployed artifact, ordered by version number. Each version includes the image reference, status, URL, and who created it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listVersionsInput) (*mcp.CallToolResult, *listVersionsOutput, error) {
		versions, err := orch.ListVersions(ctx, input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		return nil, &listVersionsOutput{
			ArtifactID: input.ArtifactID,
			Versions:   versions,
		}, nil
	})
}

type rollbackArtifactInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to roll back"`
	Version    int    `json:"version" jsonschema:"Target version number to roll back to"`
}

type rollbackArtifactOutput struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Target     string `json:"target"`
	Status     string `json:"status"`
	ImageRef   string `json:"image_ref"`
	NewVersion int    `json:"new_version"`
	Message    string `json:"message"`
}

func registerRollbackTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "rollback_artifact",
		Description: "Roll back a deployed artifact to a previous version. This redeploys the artifact using the image and configuration from the specified version snapshot. A new version entry is created for the rollback (history is not rewritten).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input rollbackArtifactInput) (*mcp.CallToolResult, *rollbackArtifactOutput, error) {
		result, err := orch.Rollback(ctx, input.ArtifactID, input.Version)
		if err != nil {
			return nil, nil, err
		}

		// Fetch the artifact to get the new version number
		artifact, _ := orch.Status(ctx, result.ArtifactID)
		newVersion := 0
		if artifact != nil {
			newVersion = artifact.Version
		}

		return nil, &rollbackArtifactOutput{
			ArtifactID: result.ArtifactID,
			Name:       result.Name,
			URL:        result.URL,
			Target:     result.Target,
			Status:     result.Status,
			ImageRef:   result.ImageRef,
			NewVersion: newVersion,
			Message:    fmt.Sprintf("Successfully rolled back to version %d. New version is v%d.", input.Version, newVersion),
		}, nil
	})
}
