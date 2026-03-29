package mcp

import (
	"context"
	"time"

	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type createShareLinkInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to create a share link for"`
	Password   string `json:"password,omitempty" jsonschema:"Optional password to protect the share link"`
	ExpiresIn  string `json:"expires_in,omitempty" jsonschema:"Optional expiration duration (e.g. '24h', '7d'). Empty means no expiration."`
}

func registerCreateShareLinkTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_share_link",
		Description: "Create a public shareable link for an artifact. Anyone with the link (and optional password) can view the artifact's status and URL without a vibeD account.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input createShareLinkInput) (*mcp.CallToolResult, *api.ShareLink, error) {
		var expiresIn time.Duration
		if input.ExpiresIn != "" {
			s := input.ExpiresIn
			// Support "7d" shorthand
			if len(s) > 1 && s[len(s)-1] == 'd' {
				if d, err := time.ParseDuration(s[:len(s)-1] + "h"); err == nil {
					expiresIn = d * 24
				}
			} else {
				expiresIn, _ = time.ParseDuration(s)
			}
		}

		link, err := orch.CreateShareLink(ctx, input.ArtifactID, input.Password, expiresIn)
		if err != nil {
			return nil, nil, err
		}
		return nil, link, nil
	})
}

type listShareLinksInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to list share links for"`
}

type listShareLinksOutput struct {
	Links []api.ShareLink `json:"links"`
}

func registerListShareLinksTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_share_links",
		Description: "List all share links for an artifact. Only the artifact owner or admin can see these.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listShareLinksInput) (*mcp.CallToolResult, *listShareLinksOutput, error) {
		links, err := orch.ListShareLinks(ctx, input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		if links == nil {
			links = []api.ShareLink{}
		}
		return nil, &listShareLinksOutput{Links: links}, nil
	})
}

type revokeShareLinkInput struct {
	Token string `json:"token" jsonschema:"The share link token to revoke"`
}

func registerRevokeShareLinkTool(server *mcp.Server, orch *orchestrator.Orchestrator) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "revoke_share_link",
		Description: "Revoke a share link so it can no longer be used. The link will return 404 after revocation.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input revokeShareLinkInput) (*mcp.CallToolResult, *map[string]string, error) {
		if err := orch.RevokeShareLink(ctx, input.Token); err != nil {
			return nil, nil, err
		}
		result := map[string]string{"status": "revoked"}
		return nil, &result, nil
	})
}
