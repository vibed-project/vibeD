package mcp

import (
	"github.com/vibed-project/vibeD/internal/audit"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates a new MCP server with all vibeD tools registered.
// deploySvc may be nil — see RegisterTools for the fallback behavior.
// auditRec may be nil (auditing disabled).
func NewServer(orch *orchestrator.Orchestrator, deploySvc *deploy.Service, limits config.LimitsConfig, userStore store.UserStore, auditRec *audit.Recorder) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "vibed",
		Version: "0.1.0",
	}, nil)

	RegisterTools(server, orch, deploySvc, limits, userStore, auditRec)

	return server
}
