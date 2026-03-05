package mcp

import (
	"github.com/maxkorbacher/vibed/internal/config"
	"github.com/maxkorbacher/vibed/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates a new MCP server with all vibeD tools registered.
func NewServer(orch *orchestrator.Orchestrator, limits config.LimitsConfig) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "vibed",
		Version: "0.1.0",
	}, nil)

	RegisterTools(server, orch, limits)

	return server
}
