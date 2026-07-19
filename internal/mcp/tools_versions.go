package mcp

import (
	"context"
	"fmt"

	"github.com/vibed-project/vibeD/internal/audit"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listVersionsInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"ID of the artifact to list versions for"`
}

type listVersionsOutput struct {
	ArtifactID string                `json:"artifact_id"`
	Versions   []api.ArtifactVersion `json:"versions"`
}

func registerListVersionsTool(server *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_versions",
		Description: "List all version snapshots for a deployed artifact, ordered by version number. Each version includes the image reference, status, URL, and who created it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listVersionsInput) (*mcp.CallToolResult, *listVersionsOutput, error) {
		if deploySvc == nil {
			versions, err := orch.ListVersions(ctx, input.ArtifactID)
			if err != nil {
				return nil, nil, err
			}
			return nil, &listVersionsOutput{
				ArtifactID: input.ArtifactID,
				Versions:   versions,
			}, nil
		}

		// VibedApp path: the deploy history lives on the CR. Get supplies the
		// app's URL and owner for the mapped records (Versions re-checks
		// ownership itself).
		owner := ownerFromContext(ctx)
		app, err := deploySvc.Get(ctx, owner, input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		recs, err := deploySvc.Versions(ctx, owner, input.ArtifactID)
		if err != nil {
			return nil, nil, err
		}
		versions := make([]api.ArtifactVersion, 0, len(recs))
		for _, rec := range recs { // already newest-first
			versions = append(versions, versionRecordToArtifactVersion(app, rec))
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

func registerRollbackTool(server *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service, auditRec *audit.Recorder) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "rollback_artifact",
		Description: "Roll back a deployed artifact to a previous version. This redeploys the artifact using the image and configuration from the specified version snapshot. A new version entry is created for the rollback (history is not rewritten).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input rollbackArtifactInput) (*mcp.CallToolResult, *rollbackArtifactOutput, error) {
		if deploySvc == nil {
			result, err := orch.Rollback(ctx, input.ArtifactID, input.Version)
			if err != nil {
				_ = auditRec.Record(ctx, "rollback", input.ArtifactID, "error", err.Error())
				return nil, nil, err
			}
			// Success-path audit: fail-closed Recorder bubbles up so the MCP
			// caller learns the rollback isn't durably logged and can retry.
			if rerr := auditRec.Record(ctx, "rollback", input.ArtifactID, "ok", fmt.Sprintf("to v%d", input.Version)); rerr != nil {
				return nil, nil, fmt.Errorf("rollback succeeded but audit failed: %w", rerr)
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
		}

		// VibedApp path: the deploy service records its own audit events (with
		// the same fail-closed semantics), so no auditRec calls here —
		// recording in both layers would double-count every rollback.
		owner := ownerFromContext(ctx)
		res, err := deploySvc.Rollback(ctx, owner, input.ArtifactID, input.Version)
		if err != nil {
			return nil, nil, err
		}
		status := "deploying"
		if res.Ready {
			status = "running"
		} else if res.Phase == vibedv1.PhaseFailed {
			status = "failed"
		}
		// The rollback appended a new history record; its number is the new
		// current version (mirrors the legacy Status re-fetch, and likewise
		// best-effort).
		newVersion := 0
		if recs, verr := deploySvc.Versions(ctx, owner, input.ArtifactID); verr == nil && len(recs) > 0 {
			newVersion = recs[0].Version // newest-first
		}
		return nil, &rollbackArtifactOutput{
			ArtifactID: res.AppID,
			Name:       res.AppID,
			URL:        res.URL,
			Target:     string(api.TargetSandbox),
			Status:     status,
			ImageRef:   "", // warm-pool apps have no per-app image
			NewVersion: newVersion,
			Message:    fmt.Sprintf("Successfully rolled back to version %d. New version is v%d.", input.Version, newVersion),
		}, nil
	})
}
