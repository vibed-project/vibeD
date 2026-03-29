package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type deployArtifactInput struct {
	Name     string            `json:"name" jsonschema:"Unique name for the artifact (lowercase and DNS-safe)"`
	Files    map[string]string `json:"files" jsonschema:"Map of relative file path to file content"`
	Language string            `json:"language,omitempty" jsonschema:"Language/framework hint (e.g. nodejs python go static)"`
	Target   string            `json:"target,omitempty" jsonschema:"Deployment target: knative kubernetes or auto (default: auto)"`
	EnvVars    map[string]string `json:"env_vars,omitempty" jsonschema:"Environment variables for the deployed artifact"`
	SecretRefs map[string]string `json:"secret_refs,omitempty" jsonschema:"Map of env var name to Kubernetes Secret reference in format 'secret-name:key'. The secret must exist in the deployment namespace. Example: {\"DB_PASSWORD\": \"my-db-creds:password\"}"`
	Port       int               `json:"port,omitempty" jsonschema:"Port the application listens on (auto-detected if not set)"`
}

func registerDeployTool(server *mcp.Server, orch *orchestrator.Orchestrator, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "deploy_artifact",
		Description: "Deploy a web artifact (website, web app) to the cluster. Provide source files and vibeD handles building a container image and deploying it. " +
			"Returns immediately with status \"building\" and an artifact_id — use get_artifact_status to poll until status is \"running\". " +
			"Knative is used when available (auto-scaling, clean URLs), otherwise falls back to plain Kubernetes. Set target explicitly to override. " +
			"For Go apps, go.mod is auto-generated if not provided.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input deployArtifactInput) (*mcp.CallToolResult, *orchestrator.DeployResult, error) {
		if err := validateFileLimits(input.Files, limits); err != nil {
			return nil, nil, err
		}

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
		return nil, result, nil
	})
}
