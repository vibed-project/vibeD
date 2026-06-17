package mcp

import (
	"github.com/vibed-project/vibeD/internal/audit"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterTools registers all vibeD MCP tools with the server.
//
// deploySvc, when non-nil, routes the core artifact lifecycle (deploy /
// update / list / status / delete) through the VibedApp path. When nil those
// fall back to the orchestrator path. Either way they agree on one backend, so
// MCP never goes split-brain. The remaining tools (logs, versions, rollback,
// share) stay on the orchestrator pending a follow-up.
func RegisterTools(server *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service, limits config.LimitsConfig, userStore store.UserStore, auditRec *audit.Recorder) {
	registerDeployTool(server, orch, deploySvc, limits)
	registerListTool(server, orch, deploySvc)
	registerStatusTool(server, orch, deploySvc)
	registerDeleteTool(server, orch, deploySvc)
	registerLogsTool(server, orch, limits)
	registerTargetsTool(server, orch)
	registerUpdateTool(server, orch, deploySvc, limits)
	registerListVersionsTool(server, orch)
	registerRollbackTool(server, orch, auditRec)
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
