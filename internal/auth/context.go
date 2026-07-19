package auth

import (
	"context"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

type contextKey string

const roleKey contextKey = "vibed-role"
const userIDKey contextKey = "vibed-user-id"
const identityKey contextKey = "vibed-identity"

// UserIDFromContext extracts the authenticated user's ID from the request context.
// The MCP SDK stores TokenInfo in context via auth.TokenInfoFromContext().
// Returns "" when auth is disabled or no user is set.
func UserIDFromContext(ctx context.Context) string {
	// Check the simple value set by WithUserID first (used for background contexts).
	if id, ok := ctx.Value(userIDKey).(string); ok && id != "" {
		return id
	}
	info := mcpauth.TokenInfoFromContext(ctx)
	if info == nil {
		return ""
	}
	return info.UserID
}

// WithUserID stores a user ID in the context. Used when creating a background context
// that must preserve the caller's identity (e.g. async deploy goroutines).
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// WithIdentity stores the request's resolved identity in the context. The auth
// middleware sets it once per request after token verification (see Build);
// downstream consumers (the suspended-user check, RoleMiddleware) read it via
// IdentityFromContext instead of re-querying the user store.
func WithIdentity(ctx context.Context, ident *Identity) context.Context {
	return context.WithValue(ctx, identityKey, ident)
}

// IdentityFromContext returns the identity resolved for this request, or nil
// when none was resolved (auth disabled, no user store, or an exotic
// middleware order that bypassed resolution — callers must keep their store
// fallback for that case).
func IdentityFromContext(ctx context.Context) *Identity {
	if ident, ok := ctx.Value(identityKey).(*Identity); ok {
		return ident
	}
	return nil
}

// WithRole adds the user's role to the context.
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey, role)
}

// RoleFromContext extracts the user's role from the context.
// Returns "user" as the default when no role is set.
func RoleFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(roleKey).(string); ok && v != "" {
		return v
	}
	return "user"
}

// IsAdmin returns true if the current user has the admin role.
func IsAdmin(ctx context.Context) bool {
	return RoleFromContext(ctx) == "admin"
}
