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

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/builder"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/deployer"
	"github.com/vibed-project/vibeD/internal/environment"
	"github.com/vibed-project/vibeD/internal/frontend"
	"github.com/vibed-project/vibeD/internal/health"
	"github.com/vibed-project/vibeD/internal/k8s"
	mcppkg "github.com/vibed-project/vibeD/internal/mcp"
	"github.com/vibed-project/vibeD/internal/metrics"
	"github.com/vibed-project/vibeD/internal/orchestrator"
	"github.com/vibed-project/vibeD/internal/storage"
	"github.com/vibed-project/vibeD/internal/store"
	"github.com/vibed-project/vibeD/pkg/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if transport != "" {
		cfg.Server.Transport = transport
	}

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
	switch cfg.Store.Backend {
	case "memory":
		st = store.NewMemoryStore()
	case "configmap":
		st = store.NewConfigMapStore(
			k8sClients.Clientset,
			cfg.Store.ConfigMap.Name,
			cfg.Store.ConfigMap.Namespace,
		)
	default:
		logger.Error("unsupported store backend", "backend", cfg.Store.Backend)
		os.Exit(1)
	}
	checker.SetReady("store")

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

	// Register wasmCloud deployer
	wasmDeployer := deployer.NewWasmCloudDeployer(k8sClients.DynamicClient, k8sClients.Clientset, cfg.Deployment, cfg.WasmCloud, logger)
	factory.Register(api.TargetWasmCloud, wasmDeployer)

	// Create wasm builder for wasmCloud target (shares PVC with Buildah builder)
	wasmPVCName := cfg.Builder.Buildah.PVCName
	if wasmPVCName == "" {
		wasmPVCName = "vibed-data"
	}
	wasmNs := cfg.Builder.Buildah.Namespace
	if wasmNs == "" {
		wasmNs = cfg.Deployment.Namespace
	}
	wasmPVCMountPath := filepath.Dir(cfg.Storage.Local.BasePath)
	wasmBldr := builder.NewWasmBuilder(
		k8sClients.Clientset, cfg.WasmCloud.Builder, cfg.Registry,
		wasmNs, wasmPVCName, wasmPVCMountPath, logger,
	)

	// Create orchestrator
	orch := orchestrator.NewOrchestrator(cfg, detector, bldr, wasmBldr, factory, stg, st, m, k8sClients.Clientset, logger)

	// Create MCP server
	mcpServer := mcppkg.NewServer(orch, cfg.Limits)

	// Initialize authentication middleware
	authMiddleware, err := vibedauth.Middleware(cfg.Auth, logger)
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
		runHTTPServer(ctx, cfg, mcpServer, orch, m, checker, authMiddleware, tlsConfig, logger)

	case "both":
		go runHTTPServer(ctx, cfg, mcpServer, orch, m, checker, authMiddleware, tlsConfig, logger)
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

func runHTTPServer(ctx context.Context, cfg *config.Config, mcpServer *mcp.Server, orch *orchestrator.Orchestrator, m *metrics.Metrics, checker *health.Checker, authMiddleware func(http.Handler) http.Handler, tlsConfig *tls.Config, logger *slog.Logger) {
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

	// Frontend + API
	frontendHandler := frontend.NewHandler(orch, cfg)
	mux.Handle("/", frontendHandler)

	// Build handler chain: role → auth (selective) → metrics → mux
	var handler http.Handler = mux

	// Apply auth middleware (skips health/metrics/static paths) and role middleware
	if cfg.Auth.Enabled {
		roleMap := vibedauth.BuildRoleMap(cfg.Auth.APIKeys)
		handler = vibedauth.RoleMiddleware(roleMap)(handler)       // inner: inject role into context
		handler = vibedauth.SkipAuthPaths(authMiddleware)(handler) // outer: authenticate first
	}

	// Apply metrics middleware (outermost — captures all requests)
	handler = m.HTTPMiddleware(handler)

	server := &http.Server{
		Addr:      cfg.Server.HTTPAddr,
		Handler:   handler,
		TLSConfig: tlsConfig,
	}

	go func() {
		<-ctx.Done()
		logger.Info("shutting down HTTP server")
		server.Close()
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
