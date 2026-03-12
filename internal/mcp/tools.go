package mcp

import (
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/orchestrator"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterTools registers all vibeD MCP tools with the server.
func RegisterTools(server *mcp.Server, orch *orchestrator.Orchestrator, limits config.LimitsConfig) {
	registerDeployTool(server, orch, limits)
	registerListTool(server, orch)
	registerStatusTool(server, orch)
	registerDeleteTool(server, orch)
	registerLogsTool(server, orch, limits)
	registerTargetsTool(server, orch)
	registerUpdateTool(server, orch, limits)
	registerListVersionsTool(server, orch)
	registerRollbackTool(server, orch)
	registerShareTool(server, orch)
	registerUnshareTool(server, orch)
}
