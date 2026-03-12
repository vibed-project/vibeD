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
	Target   string            `json:"target,omitempty" jsonschema:"Deployment target: knative kubernetes wasmcloud or auto (default: auto)"`
	EnvVars  map[string]string `json:"env_vars,omitempty" jsonschema:"Environment variables for the deployed artifact"`
	Port     int               `json:"port,omitempty" jsonschema:"Port the application listens on (auto-detected if not set)"`
}

func registerDeployTool(server *mcp.Server, orch *orchestrator.Orchestrator, limits config.LimitsConfig) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "deploy_artifact",
		Description: "Deploy a web artifact (website, web app) to the cluster. Provide source files and vibeD handles building a container image and deploying it. Returns the access URL. Compiled languages (Go, Rust) are automatically deployed to wasmCloud when available; other languages use Knative. Set target explicitly to override.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input deployArtifactInput) (*mcp.CallToolResult, *orchestrator.DeployResult, error) {
		if err := validateFileLimits(input.Files, limits); err != nil {
			return nil, nil, err
		}

		result, err := orch.Deploy(ctx, orchestrator.DeployRequest{
			Name:     input.Name,
			Files:    input.Files,
			Language: input.Language,
			Target:   input.Target,
			EnvVars:  input.EnvVars,
			Port:     input.Port,
		})
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
