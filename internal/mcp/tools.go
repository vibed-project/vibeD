package mcp

import (
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterTools registers all vibeD MCP tools with the server.
func RegisterTools(server *mcp.Server, orch *orchestrator.Orchestrator, limits config.LimitsConfig, userStore store.UserStore) {
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
	registerCreateShareLinkTool(server, orch)
	registerListShareLinksTool(server, orch)
	registerRevokeShareLinkTool(server, orch)
	if userStore != nil {
		registerListUsersTool(server, userStore)
		registerGetUserTool(server, userStore)
		registerListDepartmentsTool(server, userStore)
		registerCreateDepartmentTool(server, userStore)
	}
}
