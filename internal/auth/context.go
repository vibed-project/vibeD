package auth

import (
	"context"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// UserIDFromContext extracts the authenticated user's ID from the request context.
// The MCP SDK stores TokenInfo in context via auth.TokenInfoFromContext().
// Returns "" when auth is disabled or no user is set.
func UserIDFromContext(ctx context.Context) string {
	info := mcpauth.TokenInfoFromContext(ctx)
	if info == nil {
		return ""
	}
	return info.UserID
}
