package mcp

import (
	"errors"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// errDeployServiceNotConfigured is returned by every artifact-lifecycle tool
// when the deploy service is nil. buildDeployService can legitimately fail in
// production (no K8s / tarball store), in which case the /v1 deploy path is
// unavailable — the tools surface that as a clear error instead of panicking.
var errDeployServiceNotConfigured = errors.New("deploy service not configured")

// RegisterTools registers all vibeD MCP tools with the server.
//
// The artifact lifecycle (deploy / update / list / status / delete) plus logs,
// versions, rollback, and share links all run through the VibedApp deploy
// service (the /v1 path). deploySvc may be nil when its prerequisites aren't
// configured; in that case each tool returns errDeployServiceNotConfigured
// rather than panicking. The legacy orchestrator fallback — and the
// orchestrator-only tools (deployment-target discovery, per-user
// share/unshare) — were removed with the orchestrator itself.
func RegisterTools(server *mcp.Server, deploySvc *deploy.Service, limits config.LimitsConfig, userStore store.UserStore) {
	registerDeployTool(server, deploySvc, limits)
	registerListTool(server, deploySvc)
	registerStatusTool(server, deploySvc)
	registerDeleteTool(server, deploySvc)
	registerLogsTool(server, deploySvc, limits)
	registerUpdateTool(server, deploySvc, limits)
	registerListVersionsTool(server, deploySvc)
	registerRollbackTool(server, deploySvc)
	registerCreateShareLinkTool(server, deploySvc)
	registerListShareLinksTool(server, deploySvc)
	registerRevokeShareLinkTool(server, deploySvc)
	if userStore != nil {
		registerListUsersTool(server, userStore)
		registerGetUserTool(server, userStore)
		registerListDepartmentsTool(server, userStore)
		registerCreateDepartmentTool(server, userStore)
	}
}
