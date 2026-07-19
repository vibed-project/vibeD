// Package auth provides authentication for vibeD's HTTP endpoints.
//
// It supports three modes:
//   - API key authentication: simple bearer tokens validated against a configured list
//   - External OAuth: tokens verified by an external OAuth gateway/proxy
//   - OIDC: direct JWT validation against an OIDC provider (Keycloak, Azure Entra, etc.)
//
// The implementation uses the MCP SDK's auth.RequireBearerToken middleware,
// which automatically binds sessions to users and prevents session hijacking.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"
)

// Build resolves the auth provider for cfg.Mode and returns (1) the
// MCP-compatible bearer-auth middleware (SDK RequireBearerToken + a suspended-
// user check) and (2) the provider's public login routes, if any (e.g. a SAML
// SP's metadata/ACS/login endpoints). When auth is disabled it returns a
// passthrough middleware and no routes. userStore is optional (may be nil).
//
// The core registers apikey/oauth/oidc (see providers.go); an out-of-tree module
// can register additional modes via RegisterProvider. An empty mode defaults to
// apikey.
func Build(cfg config.AuthConfig, userStore store.UserStore, logger *slog.Logger) (func(http.Handler) http.Handler, []Route, error) {
	passthrough := func(next http.Handler) http.Handler { return next }
	if !cfg.Enabled {
		return passthrough, nil, nil
	}

	factory, ok := lookupProvider(cfg.Mode)
	if !ok {
		return nil, nil, fmt.Errorf("unknown auth.mode: %q (must be one of: %s)", cfg.Mode, strings.Join(Providers(), ", "))
	}
	prov, err := factory(cfg, userStore, logger)
	if err != nil {
		return nil, nil, err
	}
	if prov == nil || prov.Verifier == nil {
		return nil, nil, fmt.Errorf("auth provider %q returned no verifier", cfg.Mode)
	}

	opts := &mcpauth.RequireBearerTokenOptions{}
	tokenMiddleware := mcpauth.RequireBearerToken(prov.Verifier, opts)
	logger.Info("authentication enabled", "mode", cfg.Mode, "loginRoutes", len(prov.Routes))

	// Wrap with suspended user check
	middleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Let token auth run first (sets user identity in context)
			// We intercept by wrapping next with a status checker
			statusChecker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				userID := UserIDFromContext(r.Context())
				if userStore != nil && userID != "" {
					user, err := userStore.GetUser(r.Context(), userID)
					if err == nil && user.Status == "suspended" {
						http.Error(w, "account suspended", http.StatusUnauthorized)
						return
					}
				}
				next.ServeHTTP(w, r)
			})
			tokenMiddleware(statusChecker).ServeHTTP(w, r)
		})
	}

	return middleware, prov.Routes, nil
}

// Middleware is the routes-less form of Build, kept for callers (and tests) that
// only need the bearer-auth middleware.
func Middleware(cfg config.AuthConfig, userStore store.UserStore, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	mw, _, err := Build(cfg, userStore, logger)
	return mw, err
}

// NoAuthAdminMiddleware returns a middleware that injects the admin role into every
// request context. Used when authentication is disabled so the dashboard and all
// admin API endpoints remain fully accessible in dev/no-auth mode.
func NoAuthAdminMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Inject both an admin role AND a stable user ID so owner-scoped
			// endpoints (e.g. /v1/deploy via deploy.Service) work in no-auth
			// dev mode instead of 401-ing on a missing user.
			ctx := WithRole(r.Context(), "admin")
			ctx = WithUserID(ctx, "admin")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// publicPrefixes holds path prefixes a module registered as exempt from the
// user-auth middleware (it authenticates them itself, e.g. a SCIM token). Guarded
// because RegisterPublicPrefix runs at wiring time from the server goroutine.
var (
	publicMu       sync.Mutex
	publicPrefixes []string
)

// RegisterPublicPrefix marks a path prefix as exempt from the core user-auth
// middleware. The server calls it when mounting a module's Public route; the
// module's handler then enforces its own authentication.
func RegisterPublicPrefix(prefix string) {
	if prefix == "" {
		return
	}
	publicMu.Lock()
	defer publicMu.Unlock()
	publicPrefixes = append(publicPrefixes, prefix)
}

func isRegisteredPublic(path string) bool {
	publicMu.Lock()
	defer publicMu.Unlock()
	for _, p := range publicPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// SkipAuthPaths wraps an auth middleware to skip authentication for certain paths
// (health checks, metrics, static frontend assets, and module-registered public
// prefixes).
func SkipAuthPaths(authMiddleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		authed := authMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			// Skip auth for health, metrics, API docs, well-known endpoints, and frontend static assets
			if path == "/healthz" || path == "/readyz" || path == "/metrics" ||
				strings.HasPrefix(path, "/api/docs") ||
				strings.HasPrefix(path, "/api/share/") ||
				strings.HasPrefix(path, "/.well-known/") {
				next.ServeHTTP(w, r)
				return
			}
			// Module-registered public prefixes (e.g. /scim/v2/) — checked before
			// the protected block so a module can self-authenticate a route that
			// would otherwise fall under a protected prefix.
			if isRegisteredPublic(path) {
				next.ServeHTTP(w, r)
				return
			}
			// Protect MCP, internal /api/ endpoints, /v1/* (the OpenAPI HTTP
			// surface), and /internal/sources/ (source blobs the in-cluster
			// agent pulls with the shared token).
			if strings.HasPrefix(path, "/mcp") || strings.HasPrefix(path, "/api/") ||
				strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/internal/sources/") {
				authed.ServeHTTP(w, r)
				return
			}
			// Frontend static files: no auth needed
			next.ServeHTTP(w, r)
		})
	}
}

// apiKeyUserID is the canonical user ID for a static API key. The verifier
// (context identity), the auto-provisioned store record, and the role map all
// derive the ID from this one function so they cannot desync — the desync
// (verifier used key.Name while the record was stored under "apikey-"+key.Name)
// is exactly what made the suspended-user check miss the record and fail open.
// The prefix namespaces static keys away from OIDC subjects. An empty name
// yields an empty ID, preserving the "no identity" behavior for a nameless key.
func apiKeyUserID(name string) string {
	if name == "" {
		return ""
	}
	return "apikey-" + name
}

// apiKeyVerifier returns a TokenVerifier that validates tokens against configured API keys.
// If userStore is non-nil, it auto-provisions users on first authentication.
func apiKeyVerifier(keys []config.APIKeyConf, userStore store.UserStore, logger *slog.Logger) mcpauth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*mcpauth.TokenInfo, error) {
		for _, key := range keys {
			resolvedKey := resolveKeyValue(key.Key)
			if subtle.ConstantTimeCompare([]byte(token), []byte(resolvedKey)) == 1 {
				logger.Debug("API key authenticated",
					"name", key.Name,
					"path", req.URL.Path,
				)

				// Auto-provision user on first authentication
				if userStore != nil && key.Name != "" {
					autoProvisionAPIKeyUser(ctx, userStore, key, logger)
				}

				return &mcpauth.TokenInfo{
					Scopes:     key.Scopes,
					Expiration: time.Now().Add(24 * time.Hour),
					UserID:     apiKeyUserID(key.Name),
				}, nil
			}
		}

		// No static key matched — check runtime-generated keys in the store
		if userStore != nil {
			hash := sha256.Sum256([]byte(token))
			hashHex := hex.EncodeToString(hash[:])
			if user, err := userStore.GetUserByAPIKeyHash(ctx, hashHex); err == nil {
				logger.Debug("runtime API key authenticated", "user", user.ID, "path", req.URL.Path)
				return &mcpauth.TokenInfo{
					Scopes:     nil,
					Expiration: time.Now().Add(24 * time.Hour),
					UserID:     user.ID,
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

// autoProvisionAPIKeyUser creates a user record on first API key authentication.
func autoProvisionAPIKeyUser(ctx context.Context, userStore store.UserStore, key config.APIKeyConf, logger *slog.Logger) {
	userID := apiKeyUserID(key.Name)
	if _, err := userStore.GetUser(ctx, userID); err == nil {
		return // user already exists
	}

	role := key.Role
	if role == "" {
		role = "user"
	}

	// Resolve department if configured
	var deptID string
	if key.Department != "" {
		dept, err := userStore.GetDepartmentByName(ctx, key.Department)
		if err != nil {
			// Auto-create the department
			now := time.Now()
			dept = &api.Department{
				ID:        fmt.Sprintf("dept-%x", now.UnixNano()),
				Name:      key.Department,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if createErr := userStore.CreateDepartment(ctx, dept); createErr != nil {
				// May already exist from concurrent request; try to fetch again
				dept, _ = userStore.GetDepartmentByName(ctx, key.Department)
			}
		}
		if dept != nil {
			deptID = dept.ID
		}
	}

	now := time.Now()
	user := &api.User{
		ID:           userID,
		Name:         key.Name,
		Role:         role,
		Status:       "active",
		Provider:     "local",
		DepartmentID: deptID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := userStore.CreateUser(ctx, user); err != nil {
		logger.Debug("auto-provision user skipped (may already exist)", "name", key.Name, "error", err)
		return
	}
	logger.Info("auto-provisioned API key user", "name", key.Name, "role", role, "department", key.Department)
}

// oauthPassthroughVerifier accepts a Bearer token that must match an API key
// (acting as a proxy shared secret) and then trusts the X-Forwarded-User header
// — but ONLY when the request originates from a trusted proxy (its source IP is
// within one of trustedCIDRs). This binds the identity header to the proxy, so a
// direct client that learns the shared secret cannot spoof an arbitrary user (or
// an admin) by setting the header itself. When no trusted proxies are
// configured, the header is never trusted and every request maps to the generic
// "oauth-user" identity.
func oauthPassthroughVerifier(keys []config.APIKeyConf, trustedCIDRs []*net.IPNet, logger *slog.Logger) mcpauth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*mcpauth.TokenInfo, error) {
		if token == "" {
			return nil, mcpauth.ErrInvalidToken
		}

		valid := false
		for _, key := range keys {
			resolvedKey := resolveKeyValue(key.Key)
			if subtle.ConstantTimeCompare([]byte(token), []byte(resolvedKey)) == 1 {
				valid = true
				break
			}
		}

		if !valid {
			logger.Warn("OAuth passthrough rejected: invalid proxy secret", "path", req.URL.Path)
			return nil, mcpauth.ErrInvalidToken
		}

		// Trust the identity header only from a configured trusted proxy.
		userID := "oauth-user"
		if fwd := req.Header.Get("X-Forwarded-User"); fwd != "" {
			if remoteIPTrusted(req.RemoteAddr, trustedCIDRs) {
				userID = fwd
			} else {
				logger.Warn("OAuth passthrough: ignoring X-Forwarded-User from an untrusted source",
					"remote", req.RemoteAddr, "path", req.URL.Path)
			}
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

// parseTrustedProxies parses CIDR strings into networks, logging and skipping any
// that are malformed.
func parseTrustedProxies(cidrs []string, logger *slog.Logger) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			logger.Warn("ignoring malformed auth.trustedProxies CIDR", "value", c, "error", err)
			continue
		}
		nets = append(nets, n)
	}
	return nets
}

// remoteIPTrusted reports whether remoteAddr's IP is within any trusted CIDR.
func remoteIPTrusted(remoteAddr string, trusted []*net.IPNet) bool {
	if len(trusted) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr // no port
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// BuildRoleMap creates a mapping from the canonical API-key user ID
// (apiKeyUserID) to role, matching the identity the verifier puts in the
// context so RoleMiddleware's lookup hits. Users without an explicit role
// default to "user".
func BuildRoleMap(keys []config.APIKeyConf) map[string]string {
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		role := k.Role
		if role == "" {
			role = "user"
		}
		m[apiKeyUserID(k.Name)] = role
	}
	return m
}

// RoleMiddleware creates middleware that injects the authenticated user's role into the context.
// It checks the roleMap first (for API key users), then falls back to the user store (for OIDC users).
func RoleMiddleware(roleMap map[string]string, userStore store.UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := UserIDFromContext(r.Context())
			if userID != "" {
				role := roleMap[userID]
				if role == "" && userStore != nil {
					if u, err := userStore.GetUser(r.Context(), userID); err == nil {
						role = u.Role
					}
				}
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

// newOIDCVerifier creates a TokenVerifier that validates JWTs against an OIDC provider.
// It auto-provisions user records in the store on first login.
func newOIDCVerifier(cfg config.OIDCConfig, userStore store.UserStore, logger *slog.Logger) (mcpauth.TokenVerifier, error) {
	provider, err := oidc.NewProvider(context.Background(), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering OIDC provider %q: %w", cfg.Issuer, err)
	}

	if cfg.Audience == "" {
		return nil, fmt.Errorf("OIDC audience (client ID) must be configured to prevent cross-app token reuse")
	}

	verifierConfig := &oidc.Config{
		ClientID: cfg.Audience,
	}
	idTokenVerifier := provider.Verifier(verifierConfig)

	usernameClaim := cfg.UsernameClaim
	if usernameClaim == "" {
		usernameClaim = "preferred_username"
	}
	emailClaim := cfg.EmailClaim
	if emailClaim == "" {
		emailClaim = "email"
	}
	roleClaim := cfg.RoleClaim
	if roleClaim == "" {
		roleClaim = "realm_access.roles"
	}
	adminRole := cfg.AdminRole
	if adminRole == "" {
		adminRole = "vibed-admin"
	}

	logger.Info("OIDC authentication configured",
		"issuer", cfg.Issuer,
		"audience", cfg.Audience,
		"usernameClaim", usernameClaim,
		"adminRole", adminRole,
	)

	return func(ctx context.Context, token string, req *http.Request) (*mcpauth.TokenInfo, error) {
		idToken, err := idTokenVerifier.Verify(ctx, token)
		if err != nil {
			logger.Debug("OIDC token verification failed", "error", err, "path", req.URL.Path)
			return nil, mcpauth.ErrInvalidToken
		}

		// Extract claims
		var claims map[string]interface{}
		if err := idToken.Claims(&claims); err != nil {
			logger.Warn("OIDC claims extraction failed", "error", err)
			return nil, mcpauth.ErrInvalidToken
		}

		sub := idToken.Subject
		username := extractStringClaim(claims, usernameClaim)
		if username == "" {
			username = sub
		}
		email := extractStringClaim(claims, emailClaim)

		// Determine role from claims
		role := "user"
		roles := extractRoleClaims(claims, roleClaim)
		for _, r := range roles {
			if r == adminRole {
				role = "admin"
				break
			}
		}

		// Auto-provision user if store is available
		if userStore != nil {
			if _, err := userStore.GetUser(ctx, sub); err != nil {
				// Resolve department from claim
				var deptID string
				if cfg.DepartmentClaim != "" {
					deptName := extractStringClaim(claims, cfg.DepartmentClaim)
					if deptName != "" {
						dept, dErr := userStore.GetDepartmentByName(ctx, deptName)
						if dErr != nil {
							now := time.Now()
							dept = &api.Department{
								ID: fmt.Sprintf("dept-%x", now.UnixNano()), Name: deptName,
								CreatedAt: now, UpdatedAt: now,
							}
							if cErr := userStore.CreateDepartment(ctx, dept); cErr != nil {
								dept, _ = userStore.GetDepartmentByName(ctx, deptName)
							}
						}
						if dept != nil {
							deptID = dept.ID
						}
					}
				}

				// User doesn't exist — create
				now := time.Now()
				newUser := &api.User{
					ID:           sub,
					Name:         username,
					Email:        email,
					Role:         role,
					Status:       "active",
					Provider:     "oidc",
					DepartmentID: deptID,
					CreatedAt:    now,
					UpdatedAt:    now,
				}
				if createErr := userStore.CreateUser(ctx, newUser); createErr != nil {
					logger.Debug("auto-provision user failed (may already exist)", "sub", sub, "error", createErr)
				} else {
					logger.Info("auto-provisioned OIDC user", "sub", sub, "name", username, "role", role, "department", deptID)
				}
			}
		}

		logger.Debug("OIDC authenticated", "sub", sub, "name", username, "role", role, "path", req.URL.Path)

		return &mcpauth.TokenInfo{
			UserID:     sub,
			Expiration: idToken.Expiry,
		}, nil
	}, nil
}

// extractStringClaim extracts a string value from a claims map.
func extractStringClaim(claims map[string]interface{}, key string) string {
	if v, ok := claims[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// extractRoleClaims extracts role strings from nested claims.
// Supports dot-separated paths like "realm_access.roles".
func extractRoleClaims(claims map[string]interface{}, path string) []string {
	parts := strings.Split(path, ".")
	var current interface{} = claims

	for _, part := range parts[:len(parts)-1] {
		if m, ok := current.(map[string]interface{}); ok {
			current = m[part]
		} else {
			return nil
		}
	}

	lastKey := parts[len(parts)-1]
	if m, ok := current.(map[string]interface{}); ok {
		if arr, ok := m[lastKey].([]interface{}); ok {
			var roles []string
			for _, v := range arr {
				if s, ok := v.(string); ok {
					roles = append(roles, s)
				}
			}
			return roles
		}
	}
	return nil
}
