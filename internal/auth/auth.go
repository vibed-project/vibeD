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

	// Resolve the caller's identity ONCE per request (with a bounded TTL cache
	// in front of the user store, see identityResolver) and flow it via context.
	// The suspended-user check below and RoleMiddleware both read that identity,
	// so an authenticated request costs at most one user-store lookup — zero
	// within the cache TTL.
	resolver := newIdentityResolver(userStore, cfg.IdentityCacheTTL)

	// Wrap with identity resolution + suspended user check
	middleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Let token auth run first (sets user identity in context)
			// We intercept by wrapping next with a status checker
			statusChecker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				userID := UserIDFromContext(r.Context())
				if userID != "" {
					ident := IdentityFromContext(r.Context())
					if ident == nil || ident.ID != userID {
						if ident = resolver.Resolve(r.Context(), userID); ident != nil {
							r = r.WithContext(WithIdentity(r.Context(), ident))
						}
					}
					if ident != nil && ident.Status == "suspended" {
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
// Key secrets are resolved once at construction (see resolveAPIKeys), so the
// per-request path is a pure in-memory constant-time comparison — no filesystem
// or environment access on the hot path.
// If userStore is non-nil, it auto-provisions users on first authentication.
func apiKeyVerifier(keys []resolvedAPIKey, userStore store.UserStore, logger *slog.Logger) mcpauth.TokenVerifier {
	// Auto-provision runs at most once per key per process: the record's
	// existence never regresses (the user store has no delete), so re-checking
	// it on every request only cost an extra GetUser round-trip. First
	// authentication still performs the full existence check + create, so a
	// record that already exists (e.g. after a restart) is never re-created.
	provisionOnce := make(map[string]*sync.Once, len(keys))
	for _, key := range keys {
		if key.conf.Name != "" {
			provisionOnce[key.conf.Name] = &sync.Once{}
		}
	}
	return func(ctx context.Context, token string, req *http.Request) (*mcpauth.TokenInfo, error) {
		for _, key := range keys {
			if subtle.ConstantTimeCompare([]byte(token), []byte(key.secret)) == 1 {
				logger.Debug("API key authenticated",
					"name", key.conf.Name,
					"path", req.URL.Path,
				)

				// Auto-provision user on first authentication
				if userStore != nil && key.conf.Name != "" {
					if once := provisionOnce[key.conf.Name]; once != nil {
						once.Do(func() { autoProvisionAPIKeyUser(ctx, userStore, key.conf, logger) })
					}
				}

				return &mcpauth.TokenInfo{
					Scopes:     key.conf.Scopes,
					Expiration: time.Now().Add(24 * time.Hour),
					UserID:     apiKeyUserID(key.conf.Name),
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
//
// Like apiKeyVerifier, the proxy-secret values are resolved once at
// construction (see resolveAPIKeys), keeping the per-request path in-memory.
func oauthPassthroughVerifier(keys []resolvedAPIKey, trustedCIDRs []*net.IPNet, logger *slog.Logger) mcpauth.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*mcpauth.TokenInfo, error) {
		if token == "" {
			return nil, mcpauth.ErrInvalidToken
		}

		valid := false
		for _, key := range keys {
			if subtle.ConstantTimeCompare([]byte(token), []byte(key.secret)) == 1 {
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
// It checks the roleMap first (for API key users), then the request-scoped
// identity the auth middleware resolved (for OIDC / runtime-key users), and
// only falls back to a user-store lookup when no identity was resolved —
// defensive for middleware orders that bypass Build's resolution, preserving
// the pre-identity behavior on such paths.
func RoleMiddleware(roleMap map[string]string, userStore store.UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := UserIDFromContext(r.Context())
			if userID != "" {
				role := roleMap[userID]
				if role == "" {
					if ident := IdentityFromContext(r.Context()); ident != nil && ident.ID == userID {
						role = ident.Role
					} else if userStore != nil {
						if u, err := userStore.GetUser(r.Context(), userID); err == nil {
							role = u.Role
						}
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

// resolvedAPIKey pairs a configured API key (metadata: name/role/scopes) with
// its secret value resolved once at verifier construction, keeping "file:"
// filesystem reads and "env:" lookups off the per-request hot path. A
// consequence: file-referenced keys are read at startup, so rotating the file's
// content requires a restart to take effect.
type resolvedAPIKey struct {
	conf   config.APIKeyConf
	secret string
}

// resolveAPIKeys resolves every configured key value once using the shared
// config.ResolveSecret helper ("env:VAR_NAME", "file:/path", or literal). A key
// that fails to resolve (or resolves to empty) falls back to its literal value
// — the same fallback the per-request path applied — but the failure is now
// logged once at startup instead of passing silently on every request.
func resolveAPIKeys(keys []config.APIKeyConf, logger *slog.Logger) []resolvedAPIKey {
	resolved := make([]resolvedAPIKey, 0, len(keys))
	for _, key := range keys {
		secret, err := config.ResolveSecret(key.Key)
		if err != nil || secret == "" {
			logger.Warn("API key secret did not resolve; using the configured value as a literal key",
				"name", key.Name, "error", err)
			secret = key.Key // Fall back to literal if resolution fails
		}
		resolved = append(resolved, resolvedAPIKey{conf: key, secret: secret})
	}
	return resolved
}

// newOIDCVerifier creates a TokenVerifier that validates JWTs against an OIDC provider.
// It auto-provisions user records in the store on first login and re-syncs
// them only when the incoming claims changed since the last persisted sync
// (see oidcSyncer); syncTTL is how long an unchanged-claims fingerprint may
// skip the store, sharing auth.identityCacheTTL.
func newOIDCVerifier(cfg config.OIDCConfig, syncTTL time.Duration, userStore store.UserStore, logger *slog.Logger) (mcpauth.TokenVerifier, error) {
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

	syncer := newOIDCSyncer(userStore, cfg.DepartmentClaim, syncTTL, logger)

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

		// Provision / re-sync the user (parity with SAML: cross-provider bind
		// guard + role/email/department sync). A refused bind (the sub
		// collides with a non-OIDC account) rejects the token — and is never
		// fingerprint-cached, so it stays refused on the next request. When
		// the claims match the subject's last persisted sync (within syncTTL),
		// the store round-trips are skipped entirely.
		if userStore != nil {
			if err := syncer.sync(ctx, sub, username, email, role, claims); err != nil {
				logger.Warn("OIDC: bind refused", "sub", sub, "error", err)
				return nil, mcpauth.ErrInvalidToken
			}
		}

		logger.Debug("OIDC authenticated", "sub", sub, "name", username, "role", role, "path", req.URL.Path)

		return &mcpauth.TokenInfo{
			UserID:     sub,
			Expiration: idToken.Expiry,
		}, nil
	}, nil
}

// syncOIDCUser provisions a new OIDC user or re-syncs an existing one. It
// returns an error when sub already belongs to a non-OIDC account (cross-
// provider bind refused), so the verifier can reject the token — mirroring the
// SAML provider's guard. Re-sync writes only when role/email/department
// actually changed. persisted reports whether the store now matches the
// incoming claims; it is false when a write failed (or lost a create race) so
// the caller must not fingerprint-cache the sync and the next login retries.
func syncOIDCUser(ctx context.Context, userStore store.UserStore, sub, username, email, role, deptID string, logger *slog.Logger) (persisted bool, err error) {
	if existing, err := userStore.GetUser(ctx, sub); err == nil && existing != nil {
		if existing.Provider != "oidc" {
			return false, fmt.Errorf("refusing to bind to %s account %q", existing.Provider, sub)
		}
		changed := false
		if existing.Role != role {
			existing.Role = role
			changed = true
		}
		if email != "" && existing.Email != email {
			existing.Email = email
			changed = true
		}
		if deptID != "" && existing.DepartmentID != deptID {
			existing.DepartmentID = deptID
			changed = true
		}
		if changed {
			existing.UpdatedAt = time.Now()
			if err := userStore.UpdateUser(ctx, existing); err != nil {
				logger.Warn("OIDC: user re-sync failed", "sub", sub, "error", err)
				return false, nil
			}
		}
		return true, nil
	}
	now := time.Now()
	u := &api.User{
		ID: sub, Name: username, Email: email, Role: role,
		Status: "active", Provider: "oidc", DepartmentID: deptID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := userStore.CreateUser(ctx, u); err != nil {
		// A lost create race is fine (the row now exists); only log.
		logger.Debug("OIDC auto-provision (may already exist)", "sub", sub, "error", err)
		return false, nil
	}
	logger.Info("auto-provisioned OIDC user", "sub", sub, "name", username, "role", role, "department", deptID)
	return true, nil
}

// resolveOIDCDepartment get-or-creates the department named by departmentClaim
// and returns its id ("" when unset). The id is a deterministic dept-<slug> (as
// SAML does), so concurrent replicas converge on one row instead of the previous
// non-deterministic dept-<nanos> that could create duplicates.
func resolveOIDCDepartment(ctx context.Context, userStore store.UserStore, departmentClaim string, claims map[string]interface{}, logger *slog.Logger) string {
	if departmentClaim == "" {
		return ""
	}
	name := extractStringClaim(claims, departmentClaim)
	if name == "" {
		return ""
	}
	if dept, err := userStore.GetDepartmentByName(ctx, name); err == nil && dept != nil {
		return dept.ID
	}
	now := time.Now()
	dept := &api.Department{ID: "dept-" + slugify(name), Name: name, CreatedAt: now, UpdatedAt: now}
	if err := userStore.CreateDepartment(ctx, dept); err != nil {
		if existing, e := userStore.GetDepartmentByName(ctx, name); e == nil && existing != nil {
			return existing.ID
		}
		logger.Warn("OIDC: resolving department", "name", name, "error", err)
		return ""
	}
	return dept.ID
}

// slugify lowercases s and keeps only [a-z0-9-], collapsing other runs to '-'.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
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
