package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/builder"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/events"
	"github.com/vibed-project/vibeD/internal/frontend"
	"github.com/vibed-project/vibeD/internal/gc"
	"github.com/vibed-project/vibeD/internal/health"
	"github.com/vibed-project/vibeD/internal/k8s"
	mcppkg "github.com/vibed-project/vibeD/internal/mcp"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/middleware"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/internal/store"
	vibedtracing "github.com/vibed-project/vibeD/internal/tracing"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	knversioned "knative.dev/serving/pkg/client/clientset/versioned"
)

func main() {
	var (
		configPath string
		transport  string
	)
	flag.StringVar(&configPath, "config", "", "Path to vibed.yaml config file")
	flag.StringVar(&transport, "transport", "", "Override transport: stdio, http, or both")
	flag.Parse()

	// Bootstrap logger for config loading (always text, info level).
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if transport != "" {
		cfg.Server.Transport = transport
	}

	// Replace logger with configured format and level.
	logger = newLogger(cfg.Server)

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

	var bldr builder.Builder
	switch cfg.Builder.Engine {
	case "pack":
		bldr = builder.NewPackBuilder(cfg.Builder, logger)
	case "buildah", "":
		ns := cfg.Builder.Buildah.Namespace
		if ns == "" {
			ns = cfg.Deployment.Namespace
		}
		pvcName := cfg.Builder.Buildah.PVCName
		if pvcName == "" {
			pvcName = "vibed-data"
		}
		// PVC mount point is the parent of the storage base path
		// (e.g. /data/vibed when basePath is /data/vibed/artifacts)
		pvcMountPath := filepath.Dir(cfg.Storage.Local.BasePath)
		bldr = builder.NewBuildahBuilder(
			k8sClients.Clientset, cfg.Builder.Buildah, cfg.Registry,
			ns, pvcName, pvcMountPath, logger,
		)
	default:
		logger.Error("unsupported builder engine", "engine", cfg.Builder.Engine)
		os.Exit(1)
	}

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
	var st store.ArtifactStore
	var userStore store.UserStore // non-nil only for SQLite backend
	switch cfg.Store.Backend {
	case "memory":
		st = store.NewMemoryStore()
	case "configmap":
		st = store.NewConfigMapStore(
			k8sClients.Clientset,
			cfg.Store.ConfigMap.Name,
			cfg.Store.ConfigMap.Namespace,
		)
	case "sqlite":
		sqliteStore, err := store.NewSQLiteStore(cfg.Store.SQLite.Path)
		if err != nil {
			logger.Error("failed to open SQLite store", "error", err, "path", cfg.Store.SQLite.Path)
			os.Exit(1)
		}
		defer sqliteStore.Close()
		st = sqliteStore
		userStore = sqliteStore // SQLiteStore implements both interfaces
	default:
		logger.Error("unsupported store backend", "backend", cfg.Store.Backend)
		os.Exit(1)
	}
	checker.SetReady("store")

	// Bootstrap API key users into user store
	if userStore != nil && cfg.Auth.Enabled && (cfg.Auth.Mode == "apikey" || cfg.Auth.Mode == "") {
		bootstrapAPIKeyUsers(cfg.Auth.APIKeys, userStore, logger)
	}

	// Initialize deployers
	factory := deployer.NewFactory()

	// Register Knative deployer
	knClient, err := knversioned.NewForConfig(k8sClients.RestConfig)
	if err != nil {
		logger.Warn("failed to create Knative client (Knative may not be installed)", "error", err)
	} else {
		knDeployer := deployer.NewKnativeDeployer(knClient, k8sClients.Clientset, cfg.Deployment, cfg.Knative, logger)
		factory.Register(api.TargetKnative, knDeployer)
	}

	// Register Kubernetes deployer
	k8sDeployer := deployer.NewKubernetesDeployer(k8sClients.Clientset, cfg.Deployment, logger)
	factory.Register(api.TargetKubernetes, k8sDeployer)

	// Create orchestrator
	// Create event bus for SSE streaming
	bus := events.NewEventBus()

	// ShareLinkStore is only available with SQLite backend
	var shareLinkStore store.ShareLinkStore
	if sls, ok := st.(store.ShareLinkStore); ok {
		shareLinkStore = sls
	}

	orch := orchestrator.NewOrchestrator(cfg, detector, bldr, factory, stg, st, m, k8sClients.Clientset, bus, shareLinkStore, logger)

	// Start garbage collector
	if cfg.GC.Enabled {
		collector, err := gc.NewGarbageCollector(
			k8sClients.Clientset, st, cfg.Deployment.Namespace,
			cfg.GC, m, logger,
		)
		if err != nil {
			logger.Error("failed to create garbage collector", "error", err)
			os.Exit(1)
		}
		// GC runs in background; ctx from signal.NotifyContext will stop it on shutdown.
		// We create ctx early here so GC can start before the transport blocks.
		gcCtx, gcCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer gcCancel()
		go collector.Run(gcCtx)
	}

	// Create MCP server
	mcpServer := mcppkg.NewServer(orch, cfg.Limits, userStore)

	// Initialize authentication middleware
	authMiddleware, err := vibedauth.Middleware(cfg.Auth, userStore, logger)
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
		runHTTPServer(ctx, cfg, mcpServer, orch, m, checker, bus, authMiddleware, tlsConfig, userStore, logger)

	case "both":
		go runHTTPServer(ctx, cfg, mcpServer, orch, m, checker, bus, authMiddleware, tlsConfig, userStore, logger)
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

func runHTTPServer(ctx context.Context, cfg *config.Config, mcpServer *mcp.Server, orch *orchestrator.Orchestrator, m *metrics.Metrics, checker *health.Checker, bus *events.EventBus, authMiddleware func(http.Handler) http.Handler, tlsConfig *tls.Config, userStore store.UserStore, logger *slog.Logger) {
	mux := http.NewServeMux()

	// Health check endpoints (always unauthenticated)
	mux.HandleFunc("/healthz", checker.LivenessHandler())
	mux.HandleFunc("/readyz", checker.ReadinessHandler())

	// Prometheus metrics endpoint (always unauthenticated)
	mux.Handle("/metrics", promhttp.Handler())

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

	// Frontend + API
	frontendHandler := frontend.NewHandler(orch, cfg, bus, m, userStore)
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

// newLogger creates a slog.Logger based on the server configuration.
func newLogger(cfg config.ServerConfig) *slog.Logger {
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

// securityHeadersMiddleware adds standard security headers to all responses.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
