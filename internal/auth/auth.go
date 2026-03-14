// Package auth provides authentication for vibeD's HTTP endpoints.
//
// It supports two modes:
//   - API key authentication: simple bearer tokens validated against a configured list
//   - External OAuth: tokens verified by an external OAuth gateway/proxy
//
// The implementation uses the MCP SDK's auth.RequireBearerToken middleware,
// which automatically binds sessions to users and prevents session hijacking.
package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vibed-project/vibeD/internal/config"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// Middleware creates the MCP-compatible auth middleware from the config.
// It wraps the SDK's auth.RequireBearerToken with a custom TokenVerifier
// that validates against configured API keys or external OAuth.
func Middleware(cfg config.AuthConfig, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	if !cfg.Enabled {
		// Return a no-op middleware when auth is disabled
		return func(next http.Handler) http.Handler {
			return next
		}, nil
	}

	var verifier mcpauth.TokenVerifier

	switch cfg.Mode {
	case "apikey", "":
		if len(cfg.APIKeys) == 0 {
			return nil, fmt.Errorf("auth.mode is 'apikey' but no API keys are configured")
		}
		verifier = apiKeyVerifier(cfg.APIKeys, logger)

	case "oauth":
		// OAuth mode: accept any well-formed Bearer token and pass it through.
		// In a production setup, this would verify JWT signatures against a JWKS endpoint.
		// For now, it allows integration with external OAuth providers via a reverse proxy.
		verifier = oauthPassthroughVerifier(logger)

	default:
		return nil, fmt.Errorf("unknown auth.mode: %q (must be 'apikey' or 'oauth')", cfg.Mode)
	}

	opts := &mcpauth.RequireBearerTokenOptions{}

	middleware := mcpauth.RequireBearerToken(verifier, opts)
	logger.Info("authentication enabled", "mode", cfg.Mode)

	return middleware, nil
}

// SkipAuthPaths wraps an auth middleware to skip authentication for certain paths
// (health checks, metrics, static frontend assets).
func SkipAuthPaths(authMiddleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		authed := authMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			// Skip auth for health, metrics, API docs, and frontend static assets
			if path == "/healthz" || path == "/readyz" || path == "/metrics" ||
				strings.HasPrefix(path, "/api/docs") {
				next.ServeHTTP(w, r)
				return
			}
			// Protect MCP and API endpoints
			if strings.HasPrefix(path, "/mcp") || strings.HasPrefix(path, "/api/") {
				authed.ServeHTTP(w, r)
				return
			}
			// Frontend static files: no auth needed
			next.ServeHTTP(w, r)
		})
	}
}

// apiKeyVerifier returns a TokenVerifier that validates tokens against configured API keys.
func apiKeyVerifier(keys []config.APIKeyConf, logger *slog.Logger) mcpauth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*mcpauth.TokenInfo, error) {
		for _, key := range keys {
			resolvedKey := resolveKeyValue(key.Key)
			if subtle.ConstantTimeCompare([]byte(token), []byte(resolvedKey)) == 1 {
				logger.Debug("API key authenticated",
					"name", key.Name,
					"path", req.URL.Path,
				)
				return &mcpauth.TokenInfo{
					Scopes:     key.Scopes,
					Expiration: time.Now().Add(24 * time.Hour), // API keys don't expire per-request
					UserID:     key.Name,
				}, nil
			}
		}

		logger.Warn("authentication failed: invalid API key",
			"path", req.URL.Path,
			"remote", req.RemoteAddr,
		)
		return nil, mcpauth.ErrInvalidToken
	}
}

// oauthPassthroughVerifier accepts any Bearer token and passes it through.
// This is intended for setups where an external gateway/proxy validates OAuth tokens
// and vibeD trusts the proxy's authentication.
func oauthPassthroughVerifier(logger *slog.Logger) mcpauth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*mcpauth.TokenInfo, error) {
		if token == "" {
			return nil, mcpauth.ErrInvalidToken
		}

		// Extract user ID from X-Forwarded-User header if set by the proxy
		userID := req.Header.Get("X-Forwarded-User")
		if userID == "" {
			userID = "oauth-user"
		}

		logger.Debug("OAuth passthrough authenticated",
			"user", userID,
			"path", req.URL.Path,
		)

		return &mcpauth.TokenInfo{
			UserID:     userID,
			Expiration: time.Now().Add(1 * time.Hour),
		}, nil
	}
}

// BuildRoleMap creates a mapping from user ID (APIKey Name) to role.
// Users without an explicit role default to "user".
func BuildRoleMap(keys []config.APIKeyConf) map[string]string {
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		role := k.Role
		if role == "" {
			role = "user"
		}
		m[k.Name] = role
	}
	return m
}

// RoleMiddleware creates middleware that injects the authenticated user's role into the context.
// It must run after the MCP auth middleware has set the UserID in context.
func RoleMiddleware(roleMap map[string]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := UserIDFromContext(r.Context())
			if userID != "" {
				role := roleMap[userID]
				if role == "" {
					role = "user"
				}
				ctx := WithRole(r.Context(), role)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// resolveKeyValue resolves an API key value using the shared config.ResolveSecret helper.
// Supports "env:VAR_NAME" and "file:/path" patterns, or literal values.
func resolveKeyValue(key string) string {
	resolved, err := config.ResolveSecret(key)
	if err != nil || resolved == "" {
		return key // Fall back to literal if resolution fails
	}
	return resolved
}
