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
// deploySvc, when non-nil, routes the artifact lifecycle (deploy / update /
// list / status / delete) plus logs, versions, rollback, and public share
// links through the VibedApp path. When nil those fall back to the
// orchestrator path. Either way they all agree on one backend, so MCP never
// goes split-brain. Two tool groups stay orchestrator-only: deployment-target
// discovery (the /v1 path has no target/template enumeration yet — GET
// /v1/templates is a stub pending milestone C) and per-user share/unshare
// grants (VibedApp has no SharedWith model).
func RegisterTools(server *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service, limits config.LimitsConfig, userStore store.UserStore, auditRec *audit.Recorder) {
	registerDeployTool(server, orch, deploySvc, limits)
	registerListTool(server, orch, deploySvc)
	registerStatusTool(server, orch, deploySvc)
	registerDeleteTool(server, orch, deploySvc)
	registerLogsTool(server, orch, deploySvc, limits)
	registerTargetsTool(server, orch)
	registerUpdateTool(server, orch, deploySvc, limits)
	registerListVersionsTool(server, orch, deploySvc)
	registerRollbackTool(server, orch, deploySvc, auditRec)
	registerShareTool(server, orch)
	registerUnshareTool(server, orch)
	registerCreateShareLinkTool(server, orch, deploySvc)
	registerListShareLinksTool(server, orch, deploySvc)
	registerRevokeShareLinkTool(server, orch, deploySvc)
	if userStore != nil {
		registerListUsersTool(server, userStore)
		registerGetUserTool(server, userStore)
		registerListDepartmentsTool(server, userStore)
		registerCreateDepartmentTool(server, userStore)
	}
}
