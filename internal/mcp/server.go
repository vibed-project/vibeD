package mcp

import (
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates a new MCP server with all vibeD tools registered.
func NewServer(orch *orchestrator.Orchestrator, limits config.LimitsConfig, userStore store.UserStore) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "vibed",
		Version: "0.1.0",
	}, nil)

	RegisterTools(server, orch, limits, userStore)

	return server
}
