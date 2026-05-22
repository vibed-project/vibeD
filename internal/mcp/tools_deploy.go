package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// deployArtifactOutput is the unified MCP deploy response across both the
// VibedApp path and the legacy orchestrator path.
type deployArtifactOutput struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
	Status     string `json:"status"`
}

type deployArtifactInput struct {
	Name       string            `json:"name" jsonschema:"Unique name for the artifact (lowercase and DNS-safe)"`
	Files      map[string]string `json:"files" jsonschema:"Map of relative file path to file content. Tip: Provide a 'Dockerfile' at the root to completely customize the build environment. If no Dockerfile is provided, a standard one is generated."`
	Language   string            `json:"language,omitempty" jsonschema:"Language/framework hint (e.g. nodejs python go static)"`
	Target     string            `json:"target,omitempty" jsonschema:"Deployment target: sandbox, kubernetes, or auto (default: auto). Use 'sandbox' for isolated, stateful workloads."`
	EnvVars    map[string]string `json:"env_vars,omitempty" jsonschema:"Environment variables for the deployed artifact"`
	SecretRefs map[string]string `json:"secret_refs,omitempty" jsonschema:"Map of env var name to Kubernetes Secret reference in format 'secret-name:key'. The secret must exist in the deployment namespace. Example: {\"DB_PASSWORD\": \"my-db-creds:password\"}"`
	Port       int               `json:"port,omitempty" jsonschema:"Port the application listens on (auto-detected if not set)"`
}

func registerDeployTool(server *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "deploy_artifact",
		Description: "Deploy a web artifact (website, web app) to the cluster. Provide source files and vibeD classifies the runtime, claims a warm sandbox, and injects the source — no per-deploy container build. " +
			"Returns an artifact_id and, once ready, a public URL; if it isn't ready within the deploy budget, poll get_artifact_status. " +
			"The runtime (and template) are auto-detected from the source; set target only to override.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input deployArtifactInput) (*mcp.CallToolResult, *deployArtifactOutput, error) {
		if err := validateFileLimits(input.Files, limits); err != nil {
			return nil, nil, err
		}

		// Legacy fallback: no VibedApp deploy service configured (e.g. no
		// K8s/tarball store) — use the orchestrator build-and-deploy path so
		// the lifecycle tools stay consistent with each other.
		if deploySvc == nil {
			result, err := orch.AsyncDeploy(ctx, orchestrator.DeployRequest{
				Name:       input.Name,
				Files:      input.Files,
				Language:   input.Language,
				Target:     input.Target,
				EnvVars:    input.EnvVars,
				SecretRefs: input.SecretRefs,
				Port:       input.Port,
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

		// VibedApp path: pack the file map into a tarball and hand it to the
		// deploy service, which classifies, stores, creates the CR, and
		// waits for Ready.
		tar, err := tarballFromFiles(input.Files)
		if err != nil {
			return nil, nil, err
		}
		req := deploy.Request{
			Name:    input.Name,
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
			Name:       input.Name,
			URL:        res.URL,
			Status:     status,
		}, nil
	})
}
