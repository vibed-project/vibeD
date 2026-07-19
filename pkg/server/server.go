// Package server wires and runs the vibeD control-plane process: tracing,
// metrics, Kubernetes clients, storage, the artifact store, the orchestrator,
// the deploy service, the MCP server, authentication, and the HTTP server.
//
// It is the reusable entry point shared by cmd/vibed and any out-of-tree binary:
// those import this package, register their providers via the internal/auth and
// internal/store registries, and call Run. The build/wiring logic lives here so
// a second binary does not have to fork main().
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/vibed-project/vibeD/internal/audit"
	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/classifier"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deploy"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/frontend"
	"github.com/vibed-project/vibeD/internal/gc"
	"github.com/vibed-project/vibeD/internal/health"
	"github.com/vibed-project/vibeD/internal/k8s"
	mcppkg "github.com/vibed-project/vibeD/internal/mcp"
	"github.com/vibed-project/vibeD/internal/meter"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/middleware"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/policy"
	"github.com/vibed-project/vibeD/internal/quota"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/internal/tarball"
	"github.com/vibed-project/vibeD/internal/tenant"
	vibedtracing "github.com/vibed-project/vibeD/internal/tracing"
	"github.com/vibed-project/vibeD/pkg/api"
	vibedhttp "github.com/vibed-project/vibeD/pkg/vibedapi/http"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Main is the standard vibeD program entry point: it parses the -config and
// -transport flags, loads and validates the config, builds the logger, and runs
// the server. cmd/vibed calls it, and so can an out-of-tree binary whose main()
// blank-imports its provider packages (registering backends/modes via init())
// and then calls server.Main(). This is the seam that lets a separate module
// boot vibeD without importing internal/.
func Main() {
	var (
		configPath string
		transport  string
	)
	flag.StringVar(&configPath, "config", "", "Path to vibed.yaml config file")
	flag.StringVar(&transport, "transport", "", "Override transport: stdio, http, or both")
	flag.Parse()

	// Bootstrap logger for config loading (always text, info level).
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := LoadConfig(configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if transport != "" {
		cfg.Server.Transport = transport
	}

	// Replace the bootstrap logger with the configured format and level.
	logger = NewLogger(cfg.Server)
	Run(cfg, logger)
}

// LoadConfig loads and validates a vibeD config file (an empty path yields the
// built-in defaults). It is exposed so an out-of-tree binary can build a custom
// bootstrap without importing internal/config: assign the result with := and it
// never has to name the config type.
func LoadConfig(path string) (*config.Config, error) {
	// Teach the config validator about store backends and auth modes registered
	// out of tree via pkg/plugin. Their provider init()s have already run by the
	// time any binary reaches this call, so the registries are populated;
	// without this the validator would reject a registered store.backend /
	// auth.mode before the registry is ever consulted. Built-ins remain valid
	// when nothing is registered.
	config.SetStoreBackendLister(store.Backends)
	config.SetAuthModeLister(vibedauth.Providers)
	return config.Load(path)
}

// Run wires every subsystem from cfg and serves until the process receives
// SIGINT/SIGTERM. It logs and calls os.Exit(1) on a fatal initialization error,
// matching the historical behavior of cmd/vibed's main().
func Run(cfg *config.Config, logger *slog.Logger) {
	// Initialize tracing (before any other subsystem so spans propagate)
	tracingShutdown, err := vibedtracing.Init(cfg.Tracing, logger)
	if err != nil {
		logger.Error("failed to initialize tracing", "error", err)
		os.Exit(1)
	}
	defer tracingShutdown(context.Background())

	logger.Info("starting vibeD",
		"transport", cfg.Server.Transport,
		"namespace", cfg.Deployment.Namespace,
		"storage", cfg.Storage.Backend,
		"auth", cfg.Auth.Enabled,
		"tls", cfg.Auth.TLS.Enabled,
	)

	// Fail fast on the auth-disabled-on-a-public-bind misconfiguration (#55):
	// no-auth mode treats every request as admin, so a non-loopback bind would
	// expose the full admin API unauthenticated. Requires an explicit
	// auth.devInsecure opt-in to proceed.
	if err := checkNoAuthBindSafety(cfg); err != nil {
		logger.Error("insecure configuration", "error", err)
		os.Exit(1)
	}
	if !cfg.Auth.Enabled && !isLoopbackBind(cfg.Server.HTTPAddr) {
		// Reached only with auth.devInsecure=true; make the posture loud on every boot.
		logger.Warn("SECURITY: auth is disabled on a non-loopback bind (auth.devInsecure=true) — every request is treated as admin; ensure this listener is network-isolated (NetworkPolicy/loopback) and never reachable by untrusted clients",
			"httpAddr", cfg.Server.HTTPAddr)
	}

	// Initialize metrics and health checker
	m := metrics.New()
	checker := health.NewChecker()

	// Initialize Kubernetes clients
	checker.SetNotReady("kubernetes", "connecting")
	k8sClients, err := k8s.NewClients(cfg.Kubernetes)
	if err != nil {
		logger.Error("failed to create k8s clients", "error", err)
		os.Exit(1)
	}
	checker.SetReady("kubernetes")

	// Initialize subsystems
	detector := environment.NewDetector(k8sClients, logger)

	// Initialize storage
	checker.SetNotReady("storage", "initializing")
	var stg storage.Storage
	switch cfg.Storage.Backend {
	case "local":
		stg, err = storage.NewLocalStorage(cfg.Storage.Local.BasePath)
		if err != nil {
			logger.Error("failed to create local storage", "error", err)
			os.Exit(1)
		}
	case "github":
		token, err := config.ResolveSecret(cfg.Storage.GitHub.TokenFile)
		if err != nil {
			logger.Error("failed to resolve GitHub token", "error", err)
			os.Exit(1)
		}
		stg, err = storage.NewGitHubStorage(
			cfg.Storage.GitHub.Owner,
			cfg.Storage.GitHub.Repo,
			cfg.Storage.GitHub.Branch,
			token,
			cfg.Storage.Local.BasePath, // Local cache dir
		)
		if err != nil {
			logger.Error("failed to create GitHub storage", "error", err)
			os.Exit(1)
		}
	case "gitlab":
		token, err := config.ResolveSecret(cfg.Storage.GitLab.Token)
		if err != nil {
			logger.Error("failed to resolve GitLab token", "error", err)
			os.Exit(1)
		}
		stg, err = storage.NewGitLabStorage(
			cfg.Storage.GitLab.URL,
			cfg.Storage.GitLab.ProjectID,
			cfg.Storage.GitLab.Branch,
			token,
			cfg.Storage.Local.BasePath, // Local cache dir
		)
		if err != nil {
			logger.Error("failed to create GitLab storage", "error", err)
			os.Exit(1)
		}
	default:
		logger.Error("unsupported storage backend", "backend", cfg.Storage.Backend)
		os.Exit(1)
	}

	// Wrap storage with per-user routing if any API key has per-user storage configured
	if cfg.Auth.Enabled && storage.HasPerUserConfigs(cfg.Auth.APIKeys) {
		stg = storage.NewUserStorageRouter(cfg.Auth.APIKeys, stg, cfg.Storage.Local.BasePath)
		logger.Info("per-user storage routing enabled")
	}
	checker.SetReady("storage")

	// Initialize artifact store
	checker.SetNotReady("store", "initializing")
	st, err := store.New(store.Deps{
		Backend:            cfg.Store.Backend,
		SQLitePath:         cfg.Store.SQLite.Path,
		ConfigMapName:      cfg.Store.ConfigMap.Name,
		ConfigMapNamespace: cfg.Store.ConfigMap.Namespace,
		K8sClient:          k8sClients.Clientset,
		Options:            cfg.Store.Options,
		Logger:             logger,
	})
	if err != nil {
		logger.Error("failed to initialize store", "backend", cfg.Store.Backend, "error", err)
		os.Exit(1)
	}
	// Release backend resources on shutdown (SQLite closes its DB handle; the
	// memory/configmap backends don't implement io.Closer, so this is a no-op).
	if closer, ok := st.(io.Closer); ok {
		defer closer.Close()
	}
	// UserStore, AuditStore, and ShareLinkStore are optional capabilities the
	// chosen backend may also implement, feature-detected via type assertion.
	// Today only SQLite (and an out-of-tree Postgres backend) implements
	// UserStore; the memory/configmap backends leave it nil.
	var userStore store.UserStore
	if us, ok := st.(store.UserStore); ok {
		userStore = us
	}
	checker.SetReady("store")

	// Governance audit trail: reuse the persistent store when it implements
	// AuditStore (SQLite), else fall back to an in-memory log (memory/configmap
	// backends don't persist audit events).
	var auditStore store.AuditStore
	if as, ok := st.(store.AuditStore); ok {
		auditStore = as
	} else {
		auditStore = store.NewMemoryStore()
		logger.Warn("audit trail is in-memory and won't survive restarts", "backend", cfg.Store.Backend)
	}
	auditRec := audit.New(auditStore, logger, cfg.Audit.FailClosed)

	// Bootstrap API key users into user store
	if userStore != nil && cfg.Auth.Enabled && (cfg.Auth.Mode == "apikey" || cfg.Auth.Mode == "") {
		bootstrapAPIKeyUsers(cfg.Auth.APIKeys, userStore, logger)
	}

	// Initialize deployers. Knative was removed in v0.3.1 (refactor.md §1.4
	// "Not a Knative replacement"); the Sandbox + Kubernetes deployers cover
	// every supported target.

	factory := deployer.NewFactory()

	// Register Sandbox deployer
	sbDeployer := deployer.NewSandboxDeployer(k8sClients.DynamicClient, k8sClients.Clientset, logger)
	factory.Register(api.TargetSandbox, sbDeployer)

	// Register Kubernetes deployer
	k8sDeployer := deployer.NewKubernetesDeployer(k8sClients.Clientset, cfg.Deployment.ReadyTimeout, logger)
	factory.Register(api.TargetKubernetes, k8sDeployer)

	// The legacy in-process "Instant Preview" warm pool was removed in
	// v0.3.1. Warm-pool management is now the job of vibed-controller +
	// agent-sandbox (SandboxWarmPool / SandboxClaim); see internal/controller.

	// Create orchestrator
	// Create event bus for SSE streaming
	bus := events.NewEventBus()

	// ShareLinkStore is only available with SQLite backend
	var shareLinkStore store.ShareLinkStore
	if sls, ok := st.(store.ShareLinkStore); ok {
		shareLinkStore = sls
	}

	orch := orchestrator.NewOrchestrator(cfg, detector, factory, stg, st, userStore, m, k8sClients.Clientset, bus, shareLinkStore, logger)

	// Lifecycle context shared by GC and orchestrator's async goroutines so
	// SIGTERM cancels in-flight builds and the GC loop together.
	lifeCtx, lifeCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer lifeCancel()
	orch.SetLifecycleContext(lifeCtx)

	// Start garbage collector.
	if cfg.GC.Enabled {
		collector, err := gc.NewGarbageCollector(
			k8sClients.Clientset, k8sClients.DynamicClient,
			st, cfg.Deployment.Namespace,
			cfg.GC, m, logger,
		)
		if err != nil {
			logger.Error("failed to create garbage collector", "error", err)
			os.Exit(1)
		}
		go collector.Run(lifeCtx)
	}

	// Build the VibedApp deploy service (POST /v1/deploy + the MCP core
	// lifecycle). When prerequisites aren't configured (no K8s/tarball
	// store) this is nil and both the HTTP endpoints and the MCP tools fall
	// back: HTTP returns 501, MCP uses the orchestrator build path.
	deploySvc, derr := buildDeployService(cfg, k8sClients, logger)
	if derr != nil {
		logger.Warn("VibedApp deploy path disabled; falling back to orchestrator", "error", derr)
		deploySvc = nil
	}
	if deploySvc != nil {
		deploySvc.Audit = auditRec
		deploySvc.ShareLinks = shareLinkStore // nil unless the sqlite backend is in use
		deploySvc.BaseURL = cfg.Server.BaseURL
		if cfg.Quotas.Enabled {
			ns := deploySvc.Namespace
			if ns == "" {
				ns = "vibed-apps"
			}
			deploySvc.Quota = quota.NewEnforcer(deploySvc.Client, userStore, ns, cfg.Quotas)
			logger.Info("deploy quotas enabled",
				"maxAppsPerOwner", cfg.Quotas.MaxAppsPerOwner,
				"maxAppsPerDepartment", cfg.Quotas.MaxAppsPerDepartment)
		}
		// Tenant resolver: the single-tenant default (every request runs in the
		// apps namespace) unless an out-of-tree module registered a multi-tenant
		// resolver.
		resolver, terr := tenant.Build(tenant.Deps{Default: tenant.Tenant{Namespace: deploySvc.Namespace}})
		if terr != nil {
			logger.Error("failed to build tenant resolver", "error", terr)
			os.Exit(1)
		}
		deploySvc.Tenants = resolver

		// Deploy-time policy gate (none by default) and usage meter (Prometheus
		// by default) — an out-of-tree module may register its own.
		gate, gerr := policy.Build(policy.Deps{})
		if gerr != nil {
			logger.Error("failed to build policy gate", "error", gerr)
			os.Exit(1)
		}
		deploySvc.Policy = gate

		sink, merr := meter.Build(meter.Deps{})
		if merr != nil {
			logger.Error("failed to build usage meter", "error", merr)
			os.Exit(1)
		}
		deploySvc.Meter = sink
		if gate != nil {
			logger.Info("deploy policy gate enabled")
		}
	}

	// Create MCP server
	mcpServer := mcppkg.NewServer(orch, deploySvc, cfg.Limits, userStore, auditRec)

	// Initialize authentication middleware and any public login routes the
	// selected provider contributes (e.g. a SAML SP's /saml/metadata + /saml/acs).
	authMiddleware, authRoutes, err := vibedauth.Build(cfg.Auth, userStore, logger)
	if err != nil {
		logger.Error("failed to initialize authentication", "error", err)
		os.Exit(1)
	}

	// Initialize TLS configuration
	tlsConfig, err := vibedauth.NewTLSConfig(cfg.Auth.TLS, logger)
	if err != nil {
		logger.Error("failed to initialize TLS", "error", err)
		os.Exit(1)
	}

	// Mark server as ready
	checker.SetReady("server")

	// Run based on transport mode
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch cfg.Server.Transport {
	case "stdio":
		logger.Info("starting MCP server on stdio")
		if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
			logger.Error("stdio server error", "error", err)
			os.Exit(1)
		}

	case "http":
		runHTTPServer(ctx, cfg, mcpServer, orch, deploySvc, auditRec, m, checker, bus, authMiddleware, authRoutes, tlsConfig, userStore, k8sClients, logger)
	case "both":
		go runHTTPServer(ctx, cfg, mcpServer, orch, deploySvc, auditRec, m, checker, bus, authMiddleware, authRoutes, tlsConfig, userStore, k8sClients, logger)
		logger.Info("starting MCP server on stdio")
		if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
			logger.Error("stdio server error", "error", err)
			os.Exit(1)
		}

	default:
		logger.Error("unknown transport", "transport", cfg.Server.Transport)
		os.Exit(1)
	}
}

func runHTTPServer(ctx context.Context, cfg *config.Config, mcpServer *mcp.Server, orch *orchestrator.Orchestrator, deploySvc *deploy.Service, auditRec *audit.Recorder, m *metrics.Metrics, checker *health.Checker, bus *events.EventBus, authMiddleware func(http.Handler) http.Handler, authRoutes []vibedauth.Route, tlsConfig *tls.Config, userStore store.UserStore, k8sClients *k8s.Clients, logger *slog.Logger) {
	mux := http.NewServeMux()

	// Mount the auth provider's public login routes (e.g. a SAML SP's
	// metadata/ACS/login). They sit on public paths so SkipAuthPaths lets them
	// through without a bearer token — they are how a session is obtained.
	for _, rt := range authRoutes {
		mux.Handle(rt.Pattern, rt.Handler)
		logger.Info("mounted auth login route", "pattern", rt.Pattern)
	}

	// /v1/* HTTP API + /healthz, /readyz, /metrics are mounted via
	// oapi-codegen's HandlerFromMux so the OpenAPI spec is the single source
	// of truth for routing. Stubs for the /v1/* business endpoints return
	// 501 until later milestones wire them; ops endpoints delegate to the
	// real handlers.
	vibedAPI := vibedhttp.New(
		http.HandlerFunc(checker.LivenessHandler()),
		http.HandlerFunc(checker.ReadinessHandler()),
		promhttp.Handler(),
		logger,
	)
	// Wire the VibedApp deploy path (POST /v1/deploy + /v1/apps), built once
	// in main() and shared with the MCP server. nil = prerequisites absent;
	// the endpoints stay 501 and the rest of vibeD still boots.
	if deploySvc != nil {
		vibedAPI.Deploy = deploySvc
		vibedAPI.MaxConcurrentLogStreamsPerUser = cfg.Limits.MaxConcurrentLogStreamsPerUser
		vibedAPI.MaxConcurrentLogStreamsGlobal = cfg.Limits.MaxConcurrentLogStreamsGlobal
		// Served tarball backend: vibeD serves the source blobs the agent pulls.
		if blob, berr := tarball.NewBlobHandler(cfg.Storage.Tarball); berr != nil {
			logger.Warn("blob handler init failed", "error", berr)
		} else if blob != nil {
			mux.Handle(tarball.BlobPathPrefix, blob)
			logger.Info("serving source blobs", "prefix", tarball.BlobPathPrefix)
		}
	}
	vibedhttp.HandlerFromMux(vibedAPI, mux)

	// Governance audit trail (admin-only). Not part of the generated OpenAPI
	// surface, so it's mounted directly; SkipAuthPaths still authenticates
	// /v1/* and the handler enforces the admin role.
	mux.HandleFunc("GET /v1/audit", auditHandler(auditRec, logger))

	// MCP HTTP endpoint
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return mcpServer },
		nil,
	)
	mux.Handle("/mcp/", mcpHandler)
	mux.Handle("/mcp", mcpHandler)

	// OAuth protected resource metadata (RFC 9728) — public, no auth required
	if cfg.Auth.Mode == "oidc" && cfg.Auth.OIDC.Issuer != "" {
		resourceURL := "http://localhost" + cfg.Server.HTTPAddr
		if cfg.Auth.TLS.Enabled {
			resourceURL = "https://localhost" + cfg.Server.HTTPAddr
		}
		mux.HandleFunc("/.well-known/oauth-protected-resource", vibedauth.OAuthMetadataHandler(cfg.Auth.OIDC, resourceURL))
	}

	// (The /preview/* reverse-proxy was removed in v0.3.1 — fast-path
	// previews now flow through vibed-router + Caddy like every other
	// app, so there's no longer a per-process proxy living here.)

	// Frontend + API
	frontendHandler := frontend.NewHandler(orch, deploySvc, cfg, bus, m, userStore, k8sClients)
	mux.Handle("/", frontendHandler)

	// Build handler chain: role → auth (selective) → metrics → mux
	var handler http.Handler = mux

	// Apply auth middleware (skips health/metrics/static paths) and role middleware
	if cfg.Auth.Enabled {
		roleMap := vibedauth.BuildRoleMap(cfg.Auth.APIKeys)
		handler = vibedauth.RoleMiddleware(roleMap, userStore)(handler) // inner: inject role into context
		handler = vibedauth.SkipAuthPaths(authMiddleware)(handler)      // outer: authenticate first
	} else {
		// Auth disabled — inject admin role so all API endpoints are accessible.
		// This makes the dashboard fully functional in no-auth (dev) mode.
		handler = vibedauth.NoAuthAdminMiddleware()(handler)
	}

	// Apply rate limiting (after auth so we can key by user)
	if cfg.Server.RateLimit.Enabled {
		handler = middleware.RateLimiter(ctx, cfg.Server.RateLimit, m)(handler)
	}

	// Apply OTel HTTP tracing middleware (extracts/injects trace context)
	if cfg.Tracing.Enabled {
		handler = otelhttp.NewHandler(handler, "vibed-http")
	}

	// Apply security headers middleware
	handler = securityHeadersMiddleware(handler)

	// Apply metrics middleware (outermost — captures all requests)
	handler = m.HTTPMiddleware(handler)

	server := &http.Server{
		Addr:              cfg.Server.HTTPAddr,
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout not set — SSE streams need long-lived writes
	}

	go func() {
		<-ctx.Done()
		logger.Info("shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown error", "error", err)
		}
	}()

	if tlsConfig != nil {
		// TLS enabled — use ListenAndServeTLS with certs from tls.Config
		scheme := "https"
		logger.Info("starting HTTPS server", "addr", cfg.Server.HTTPAddr, "scheme", scheme)
		if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "HTTPS server error: %v\n", err)
			os.Exit(1)
		}
	} else {
		logger.Info("starting HTTP server", "addr", cfg.Server.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
			os.Exit(1)
		}
	}
}

// checkNoAuthBindSafety returns a non-nil error when the server is configured
// with authentication disabled on a non-loopback bind and the operator has not
// explicitly opted in via auth.devInsecure. In that configuration every request
// is treated as admin (NoAuthAdminMiddleware), so a network-reachable bind would
// expose the full admin API unauthenticated (#55).
func checkNoAuthBindSafety(cfg *config.Config) error {
	if cfg.Auth.Enabled || cfg.Auth.DevInsecure {
		return nil
	}
	if isLoopbackBind(cfg.Server.HTTPAddr) {
		return nil
	}
	return fmt.Errorf("auth is disabled (auth.enabled=false) but the server binds a non-loopback address %q, which exposes the full admin API unauthenticated to the network; enable auth (auth.enabled=true), bind a loopback address (e.g. 127.0.0.1:8080), or set auth.devInsecure=true to override for a trusted/network-isolated environment", cfg.Server.HTTPAddr)
}

// isLoopbackBind reports whether addr (a host:port as accepted by
// http.Server.Addr) binds only the loopback interface. An empty host (":8080")
// or a wildcard (0.0.0.0, ::) binds all interfaces and is NOT loopback. A
// non-IP, non-"localhost" hostname can't be proven loopback, so it returns false
// (fail safe — the caller then requires the explicit opt-in).
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // maybe a bare host with no port
	}
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// bootstrapAPIKeyUsers seeds the user store with users from configured API keys.
// Skips users that already exist (idempotent on restart).
func bootstrapAPIKeyUsers(keys []config.APIKeyConf, userStore store.UserStore, logger *slog.Logger) {
	for _, key := range keys {
		if key.Name == "" {
			continue
		}
		// Check if user already exists
		if _, err := userStore.GetUserByName(context.Background(), key.Name); err == nil {
			continue // already exists
		}
		role := key.Role
		if role == "" {
			role = "user"
		}
		now := time.Now()
		user := &api.User{
			ID:        fmt.Sprintf("apikey-%s", key.Name),
			Name:      key.Name,
			Role:      role,
			Status:    "active",
			Provider:  "local",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := userStore.CreateUser(context.Background(), user); err != nil {
			logger.Debug("bootstrap user skipped", "name", key.Name, "error", err)
		} else {
			logger.Info("bootstrapped API key user", "name", key.Name, "role", role)
		}
	}
}

// buildDeployService assembles the /v1/deploy service: a controller-runtime
// client for VibedApp CRs, the configured tarball store, and the classifier.
// Returns an error (leaving the /v1 endpoints as 501) when prerequisites
// aren't configured, so vibeD still boots in ops-only or MCP-only mode.
func buildDeployService(cfg *config.Config, k8sClients *k8s.Clients, logger *slog.Logger) (*deploy.Service, error) {
	store, err := tarball.New(cfg.Storage.Tarball)
	if err != nil {
		return nil, fmt.Errorf("tarball store: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("scheme: %w", err)
	}
	if err := vibedv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("scheme: %w", err)
	}
	c, err := ctrlclient.New(k8sClients.RestConfig, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}

	appsNS := cfg.Deployment.AppsNamespace
	if appsNS == "" {
		appsNS = cfg.Deployment.Namespace
	}
	return &deploy.Service{
		Client:     c,
		Clientset:  k8sClients.Clientset,
		Store:      store,
		Classifier: classifier.Classifier{},
		Namespace:  appsNS,
		Metrics:    metrics.New(),
	}, nil
}

// NewLogger creates a slog.Logger based on the server configuration.
func NewLogger(cfg config.ServerConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

// auditHandler serves GET /v1/audit (admin only): the governance audit trail,
// newest first, filterable by actor/action/app and capped by limit.
func auditHandler(rec *audit.Recorder, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !vibedauth.IsAdmin(r.Context()) {
			http.Error(w, "admin role required", http.StatusForbidden)
			return
		}
		q := store.AuditQuery{
			Actor:  r.URL.Query().Get("actor"),
			Action: r.URL.Query().Get("action"),
			Target: r.URL.Query().Get("app"),
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				q.Limit = n
			}
		}
		events, err := rec.List(r.Context(), q)
		if err != nil {
			logger.Warn("audit list failed", "error", err)
			http.Error(w, "audit query failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Events []api.AuditEvent `json:"events"`
		}{Events: events})
	}
}

// securityHeadersMiddleware adds standard security headers to all responses.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
