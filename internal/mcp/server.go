package mcp

import (
	"context"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates a new MCP server with all vibeD tools registered.
// deploySvc may be nil — see RegisterTools: the lifecycle tools then return a
// clear "deploy service not configured" error.
func NewServer(deploySvc *deploy.Service, limits config.LimitsConfig, userStore store.UserStore) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "vibed",
		Version: "0.1.0",
	}, nil)

	// Count every tool call (vibed_mcp_tool_calls_total). metrics.New() is a
	// singleton, so this shares the registry with the rest of the server.
	server.AddReceivingMiddleware(toolCallMetrics(metrics.New()))

	RegisterTools(server, deploySvc, limits, userStore)

	return server
}

// toolCallMetrics counts every tools/call invocation, labeled by tool name and
// success/error. Registered as receiving middleware so it covers all tools
// without touching each handler.
func toolCallMetrics(m *metrics.Metrics) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if method == "tools/call" {
				tool := "unknown"
				if p, ok := req.GetParams().(*mcp.CallToolParams); ok && p.Name != "" {
					tool = p.Name
				}
				status := "success"
				if err != nil {
					status = "error"
				}
				m.ToolCallsTotal.WithLabelValues(tool, status).Inc()
			}
			return res, err
		}
	}
}
