package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// deployArtifactOutput is the MCP deploy response for the VibedApp path.
type deployArtifactOutput struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
	Status     string `json:"status"`

	// Reason/Message explain a "failed" status. A deploy that fails before it
	// gets a pod (e.g. no warm pool for the required template) produces no
	// logs at all, so without these an agent sees a bare "failed", finds no
	// logs, and wrongly concludes the failure was transient and retryable.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type deployArtifactInput struct {
	Name         string            `json:"name" jsonschema:"Unique name for the artifact (lowercase and DNS-safe)"`
	Files        map[string]string `json:"files" jsonschema:"Map of relative file path to file content. Tip: Provide a 'Dockerfile' at the root to completely customize the build environment. If no Dockerfile is provided, a standard one is generated."`
	Language     string            `json:"language,omitempty" jsonschema:"Language/framework hint (e.g. nodejs python go static)"`
	Target       string            `json:"target,omitempty" jsonschema:"Deployment target: sandbox, kubernetes, or auto (default: auto). Use 'sandbox' for isolated, stateful workloads."`
	EnvVars      map[string]string `json:"env_vars,omitempty" jsonschema:"Environment variables for the deployed artifact"`
	SecretRefs   map[string]string `json:"secret_refs,omitempty" jsonschema:"Map of env var name to Kubernetes Secret reference in format 'secret-name:key'. The secret must exist in the deployment namespace. Example: {\"DB_PASSWORD\": \"my-db-creds:password\"}"`
	Port         int               `json:"port,omitempty" jsonschema:"Port the application listens on (auto-detected if not set)"`
	AllowedHosts []string          `json:"allowed_hosts,omitempty" jsonschema:"Outbound hostnames the app may reach (egress allow-list), e.g. [\"api.openai.com\",\"*.example.com\"]. Default-deny: omit for no external egress."`
}

func registerDeployTool(server *mcp.Server, deploySvc *deploy.Service, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "deploy_artifact",
		Description: "Deploy a web artifact (website, web app) to the cluster. Provide source files and vibeD classifies the runtime, claims a warm sandbox, and injects the source — no per-deploy container build. " +
			"Returns an artifact_id and, once ready, a public URL; if it isn't ready within the deploy budget, poll get_artifact_status. " +
			"A \"failed\" status carries reason and message explaining why — read them before retrying; most failures (e.g. no warm pool for the required runtime) are not transient and will fail again identically. " +
			"The runtime (and template) are auto-detected from the source; set target only to override.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input deployArtifactInput) (*mcp.CallToolResult, *deployArtifactOutput, error) {
		if deploySvc == nil {
			return nil, nil, errDeployServiceNotConfigured
		}
		if err := validateFileLimits(input.Files, limits); err != nil {
			return nil, nil, err
		}

		// VibedApp path: pack the file map into a tarball and hand it to the
		// deploy service, which classifies, stores, creates the CR, and
		// waits for Ready.
		tar, err := tarballFromFiles(input.Files)
		if err != nil {
			return nil, nil, err
		}
		req := deploy.Request{
			Name:         input.Name,
			Owner:        ownerFromContext(ctx),
			Tarball:      tar,
			AllowedHosts: input.AllowedHosts,
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
			Reason:     res.Reason,
			Message:    res.Message,
		}, nil
	})
}
