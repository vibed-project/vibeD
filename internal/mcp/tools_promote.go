package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type promoteArtifactInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the fast-path preview artifact to promote to a durable build"`
}

func registerPromoteTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "promote_artifact",
		Description: "Promote a fast-path preview into a durable built artifact: runs the real container build, " +
			"deploys the digest-pinned image to a production backend, swaps the live deployment, and recycles the " +
			"pooled runner. The artifact_id stays stable; the URL changes. Returns immediately with status " +
			"\"building\" — use get_artifact_status to poll until status is \"running\".",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input promoteArtifactInput) (*mcp.CallToolResult, *orchestrator.DeployResult, error) {
		result, err := orch.AsyncPromote(ctx, input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
