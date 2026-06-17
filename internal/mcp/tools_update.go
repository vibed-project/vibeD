package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type updateArtifactInput struct {
	ArtifactID string            `json:"artifact_id" jsonschema:"ID of the artifact to update"`
	Files      map[string]string `json:"files" jsonschema:"Updated file map (full replacement of source files)"`
	EnvVars    map[string]string `json:"env_vars,omitempty" jsonschema:"Updated environment variables"`
	SecretRefs map[string]string `json:"secret_refs,omitempty" jsonschema:"Updated secret references in format 'secret-name:key'"`
}

func registerUpdateTool(server *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "update_artifact",
		Description: "Update an existing deployed artifact with new source files. Re-injects the source into the running app — no per-deploy container build. " +
			"Returns the artifact_id and, once ready, the URL; if it isn't ready within the deploy budget, poll get_artifact_status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input updateArtifactInput) (*mcp.CallToolResult, *deployArtifactOutput, error) {
		if err := validateFileLimits(input.Files, limits); err != nil {
			return nil, nil, err
		}

		// Legacy fallback: no VibedApp deploy service configured (e.g. no
		// K8s/tarball store) — use the orchestrator path so the lifecycle
		// tools stay consistent with each other.
		if deploySvc == nil {
			result, err := orch.AsyncUpdate(ctx, orchestrator.UpdateRequest{
				ArtifactID: input.ArtifactID,
				Files:      input.Files,
				EnvVars:    input.EnvVars,
				SecretRefs: input.SecretRefs,
			})
			if err != nil {
				return nil, nil, err
			}
			return nil, &deployArtifactOutput{
				ArtifactID: result.ArtifactID,
				Name:       result.Name,
				URL:        result.URL,
				Status:     result.Status,
			}, nil
		}

		// VibedApp path: a redeploy reuses the existing CR (keyed by name), so
		// packing the new source and calling Deploy with the same name updates
		// the app in place.
		tar, err := tarballFromFiles(input.Files)
		if err != nil {
			return nil, nil, err
		}
		req := deploy.Request{
			Name:    input.ArtifactID,
			Owner:   ownerFromContext(ctx),
			Tarball: tar,
		}
		for k, v := range input.EnvVars {
			req.Env = append(req.Env, vibedv1.EnvVar{Name: k, Value: v})
		}
		res, err := deploySvc.Deploy(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		status := "deploying"
		if res.Ready {
			status = "running"
		} else if res.Phase == vibedv1.PhaseFailed {
			status = "failed"
		}
		return nil, &deployArtifactOutput{
			ArtifactID: res.AppID,
			Name:       input.ArtifactID,
			URL:        res.URL,
			Status:     status,
		}, nil
	})
}
