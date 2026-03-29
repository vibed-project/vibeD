package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the complete vibeD configuration.
type Config struct {
	Organization OrganizationConfig `yaml:"organization"`
	Server       ServerConfig       `yaml:"server"`
	Auth         AuthConfig         `yaml:"auth"`
	Deployment   DeploymentConfig   `yaml:"deployment"`
	Builder      BuilderConfig      `yaml:"builder"`
	Storage      StorageConfig      `yaml:"storage"`
	Registry     RegistryConfig     `yaml:"registry"`
	Store        StoreConfig        `yaml:"store"`
	Kubernetes   KubernetesConfig   `yaml:"kubernetes"`
	Knative      KnativeConfig      `yaml:"knative"`
	Limits       LimitsConfig       `yaml:"limits"`
	GC           GCConfig           `yaml:"gc"`
	Tracing      TracingConfig      `yaml:"tracing"`
}

// TracingConfig configures OpenTelemetry distributed tracing.
type TracingConfig struct {
	Enabled    bool    `yaml:"enabled"`    // Enable tracing (default: false)
	Endpoint   string  `yaml:"endpoint"`   // OTLP gRPC endpoint (e.g. "http://jaeger:4317"); empty = stdout
	SampleRate float64 `yaml:"sampleRate"` // Sampling rate 0.0–1.0 (default: 1.0)
}

// OrganizationConfig holds the organization identity.
type OrganizationConfig struct {
	Name string `yaml:"name"` // Organization display name (e.g. "Acme Corp")
}

// LimitsConfig defines resource limits for MCP tool inputs.
type LimitsConfig struct {
	MaxTotalFileSize int `yaml:"maxTotalFileSize"` // Max total file content size in bytes (default: 50MB)
	MaxFileCount     int `yaml:"maxFileCount"`     // Max number of files per deploy/update (default: 500)
	MaxLogLines      int `yaml:"maxLogLines"`      // Max log lines per request (default: 10000)
}

// AuthConfig holds authentication and TLS settings.
type AuthConfig struct {
	Enabled bool         `yaml:"enabled"`
	Mode    string       `yaml:"mode"` // "apikey", "oauth", or "oidc"
	APIKeys []APIKeyConf `yaml:"apiKeys"`
	OIDC    OIDCConfig   `yaml:"oidc"`
	TLS     TLSConf      `yaml:"tls"`
}

// OIDCConfig configures OIDC (OpenID Connect) authentication.
type OIDCConfig struct {
	Issuer          string   `yaml:"issuer"`          // OIDC issuer URL (e.g. "https://keycloak.example.com/realms/vibed")
	Audience        string   `yaml:"audience"`        // Expected audience claim (e.g. "vibed")
	UsernameClaim   string   `yaml:"usernameClaim"`   // JWT claim for username (default: "preferred_username")
	EmailClaim      string   `yaml:"emailClaim"`      // JWT claim for email (default: "email")
	RoleClaim       string   `yaml:"roleClaim"`       // JWT claim path for roles (default: "realm_access.roles")
	AdminRole       string   `yaml:"adminRole"`       // Role value that maps to vibeD admin (default: "vibed-admin")
	DepartmentClaim string   `yaml:"departmentClaim"` // JWT claim for department name (e.g. "department")
	Scopes          []string `yaml:"scopes"`          // Scopes to advertise (default: ["openid", "profile"])
}

// APIKeyConf represents a configured API key with optional per-user storage.
type APIKeyConf struct {
	Key        string           `yaml:"key"`                  // Token value or "env:VAR_NAME"
	Name       string           `yaml:"name"`                 // Human-readable name (used as UserID)
	Scopes     []string         `yaml:"scopes"`               // Allowed scopes (empty = all)
	Role       string           `yaml:"role,omitempty"`       // "admin" or "user" (default: "user")
	Department string           `yaml:"department,omitempty"` // Auto-assign to this department on provisioning
	Storage    *UserStorageConf `yaml:"storage,omitempty"`    // Per-user storage override
}

// UserStorageConf configures per-user artifact storage.
type UserStorageConf struct {
	Backend string          `yaml:"backend"` // "github" or "gitlab"
	GitHub  *UserGitHubConf `yaml:"github,omitempty"`
	GitLab  *UserGitLabConf `yaml:"gitlab,omitempty"`
}

// UserGitHubConf is per-user GitHub storage configuration.
type UserGitHubConf struct {
	Owner  string `yaml:"owner"`
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch,omitempty"` // defaults "main"
	Token  string `yaml:"token,omitempty"`  // supports "env:VAR" and "file:PATH"
}

// UserGitLabConf is per-user GitLab storage configuration.
type UserGitLabConf struct {
	URL       string `yaml:"url,omitempty"`   // defaults "https://gitlab.com"
	ProjectID int    `yaml:"projectID"`
	Branch    string `yaml:"branch,omitempty"` // defaults "main"
	Token     string `yaml:"token,omitempty"`  // supports "env:VAR" and "file:PATH"
}

// TLSConf holds TLS certificate configuration.
type TLSConf struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"certFile"` // Path to TLS certificate
	KeyFile  string `yaml:"keyFile"`  // Path to TLS private key
	AutoTLS  bool   `yaml:"autoTLS"`  // Auto-generate self-signed cert for dev
}

type ServerConfig struct {
	Transport string          `yaml:"transport"` // "stdio", "http", or "both"
	HTTPAddr  string          `yaml:"httpAddr"`
	BaseURL   string          `yaml:"baseURL"`   // public-facing base URL for link generation, e.g. "http://localhost:8080"
	LogFormat string          `yaml:"logFormat"` // "text" (default) or "json"
	LogLevel  string          `yaml:"logLevel"`  // "debug", "info" (default), "warn", "error"
	RateLimit RateLimitConfig `yaml:"rateLimit"`
}

// RateLimitConfig configures HTTP rate limiting per client.
type RateLimitConfig struct {
	Enabled           bool    `yaml:"enabled"`           // Enable rate limiting (default: false)
	RequestsPerSecond float64 `yaml:"requestsPerSecond"` // Steady-state rate per client (default: 10)
	Burst             int     `yaml:"burst"`             // Max burst size per client (default: 20)
}

type DeploymentConfig struct {
	PreferredTarget string `yaml:"preferredTarget"` // "auto", "knative", "kubernetes"
	Namespace       string `yaml:"namespace"`
}

type BuilderConfig struct {
	Engine           string        `yaml:"engine"`           // "pack" or "buildah" (default: "buildah")
	Image            string        `yaml:"image"`            // buildpacks builder image (pack only)
	RunImage         string        `yaml:"runImage"`
	PullPolicy       string        `yaml:"pullPolicy"`
	ContainerRuntime string        `yaml:"containerRuntime"` // "auto", "docker", "podman"
	Buildah          BuildahConfig `yaml:"buildah"`
}

// BuildahConfig configures the Buildah in-cluster builder.
type BuildahConfig struct {
	Image     string `yaml:"image"`     // Buildah executor image (default: "quay.io/buildah/stable:latest")
	Namespace string `yaml:"namespace"` // Namespace for build Jobs (default: deployment.namespace)
	PVCName   string `yaml:"pvcName"`   // PVC name for shared source (default: auto from Helm)
	Timeout   string `yaml:"timeout"`   // Build timeout (default: "10m")
	Insecure  bool   `yaml:"insecure"`  // Use --tls-verify=false for non-TLS registries
}

type StorageConfig struct {
	Backend string             `yaml:"backend"` // "local", "github", or "gitlab"
	Local   LocalStorageConfig `yaml:"local"`
	GitHub  GitHubConfig       `yaml:"github"`
	GitLab  GitLabConfig       `yaml:"gitlab"`
}

type LocalStorageConfig struct {
	BasePath string `yaml:"basePath"`
}

type GitHubConfig struct {
	Owner     string `yaml:"owner"`
	Repo      string `yaml:"repo"`
	Branch    string `yaml:"branch"`
	TokenFile string `yaml:"tokenFile"`
}

// GitLabConfig holds global GitLab storage configuration.
type GitLabConfig struct {
	URL       string `yaml:"url"`       // GitLab instance URL (defaults to "https://gitlab.com")
	ProjectID int    `yaml:"projectID"` // GitLab project ID
	Branch    string `yaml:"branch"`    // Branch name (defaults to "main")
	Token     string `yaml:"token"`     // Access token or "env:VAR" / "file:PATH"
}

type RegistryConfig struct {
	Enabled  bool   `yaml:"enabled"`
	URL      string `yaml:"url"`
	Insecure bool   `yaml:"insecure"` // Use HTTP instead of HTTPS for the registry
}

type StoreConfig struct {
	Backend   string          `yaml:"backend"` // "sqlite" (default), "memory", or "configmap"
	ConfigMap ConfigMapConfig `yaml:"configmap"`
	SQLite    SQLiteConfig    `yaml:"sqlite"`
}

// SQLiteConfig configures the SQLite artifact store.
type SQLiteConfig struct {
	Path string `yaml:"path"` // Database file path (e.g. "/data/vibed.db")
}

type ConfigMapConfig struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type KubernetesConfig struct {
	Kubeconfig string `yaml:"kubeconfig"`
	Context    string `yaml:"context"`
}

type KnativeConfig struct {
	DomainSuffix string `yaml:"domainSuffix"`
	IngressClass string `yaml:"ingressClass"`
	GatewayPort  int    `yaml:"gatewayPort"` // External port for the ingress gateway (e.g. 31080 for NodePort); 0 or 80 = omitted from URLs
}

// GCConfig configures the resource garbage collector.
type GCConfig struct {
	Enabled  bool   `yaml:"enabled"`  // Enable garbage collection (default: true)
	Interval string `yaml:"interval"` // GC cycle interval (default: "1h")
	MaxAge   string `yaml:"maxAge"`   // Age threshold for orphaned resources (default: "24h")
	DryRun   bool   `yaml:"dryRun"`   // Log without deleting (default: false)
}

// Default returns a Config with sensible defaults.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Transport: "stdio",
			HTTPAddr:  ":8080",
			LogFormat: "text",
			LogLevel:  "info",
			RateLimit: RateLimitConfig{
				RequestsPerSecond: 10,
				Burst:             20,
			},
		},
		Deployment: DeploymentConfig{
			PreferredTarget: "auto",
			Namespace:       "default",
		},
		Builder: BuilderConfig{
			Engine:           "buildah",
			Image:            "paketobuildpacks/builder-jammy-base:latest",
			PullPolicy:       "if-not-present",
			ContainerRuntime: "auto",
			Buildah: BuildahConfig{
				Image:   "quay.io/buildah/stable:latest",
				Timeout: "10m",
			},
		},
		Storage: StorageConfig{
			Backend: "local",
			Local: LocalStorageConfig{
				BasePath: "/data/vibed/artifacts",
			},
			GitHub: GitHubConfig{
				Branch: "main",
			},
			GitLab: GitLabConfig{
				URL:    "https://gitlab.com",
				Branch: "main",
			},
		},
		Registry: RegistryConfig{
			Enabled: false,
		},
		Store: StoreConfig{
			Backend: "sqlite",
			ConfigMap: ConfigMapConfig{
				Name:      "vibed-artifacts",
				Namespace: "vibed-system",
			},
			SQLite: SQLiteConfig{
				Path: "/data/vibed.db",
			},
		},
		Knative: KnativeConfig{
			DomainSuffix: "127.0.0.1.sslip.io",
			IngressClass: "kourier.ingress.networking.knative.dev",
		},
		Limits: LimitsConfig{
			MaxTotalFileSize: 50 * 1024 * 1024, // 50 MB
			MaxFileCount:     500,
			MaxLogLines:      10000,
		},
		GC: GCConfig{
			Enabled:  true,
			Interval: "1h",
			MaxAge:   "24h",
			DryRun:   false,
		},
		Tracing: TracingConfig{
			SampleRate: 1.0,
		},
	}
}

// Load reads configuration from the given file path, applies environment
// variable overrides, and returns the merged config.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("reading config file: %w", err)
			}
			// File doesn't exist, use defaults
		} else {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parsing config file: %w", err)
			}
		}
	}

	applyEnvOverrides(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("VIBED_ORGANIZATION_NAME"); v != "" {
		cfg.Organization.Name = v
	}
	if v := os.Getenv("VIBED_SERVER_TRANSPORT"); v != "" {
		cfg.Server.Transport = v
	}
	if v := os.Getenv("VIBED_SERVER_HTTP_ADDR"); v != "" {
		cfg.Server.HTTPAddr = v
	}
	if v := os.Getenv("VIBED_SERVER_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
	if v := os.Getenv("VIBED_LOG_FORMAT"); v != "" {
		cfg.Server.LogFormat = v
	}
	if v := os.Getenv("VIBED_LOG_LEVEL"); v != "" {
		cfg.Server.LogLevel = v
	}
	if v := os.Getenv("VIBED_DEPLOYMENT_PREFERRED_TARGET"); v != "" {
		cfg.Deployment.PreferredTarget = v
	}
	if v := os.Getenv("VIBED_DEPLOYMENT_NAMESPACE"); v != "" {
		cfg.Deployment.Namespace = v
	}
	if v := os.Getenv("VIBED_BUILDER_IMAGE"); v != "" {
		cfg.Builder.Image = v
	}
	if v := os.Getenv("VIBED_BUILDER_ENGINE"); v != "" {
		cfg.Builder.Engine = v
	}
	if v := os.Getenv("VIBED_BUILDER_CONTAINER_RUNTIME"); v != "" {
		cfg.Builder.ContainerRuntime = v
	}
	if v := os.Getenv("VIBED_BUILDER_BUILDAH_IMAGE"); v != "" {
		cfg.Builder.Buildah.Image = v
	}
	if v := os.Getenv("VIBED_BUILDER_BUILDAH_INSECURE"); v != "" {
		cfg.Builder.Buildah.Insecure, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("VIBED_STORAGE_BACKEND"); v != "" {
		cfg.Storage.Backend = v
	}
	if v := os.Getenv("VIBED_STORAGE_LOCAL_BASE_PATH"); v != "" {
		cfg.Storage.Local.BasePath = v
	}
	if v := os.Getenv("VIBED_STORAGE_GITHUB_OWNER"); v != "" {
		cfg.Storage.GitHub.Owner = v
	}
	if v := os.Getenv("VIBED_STORAGE_GITHUB_REPO"); v != "" {
		cfg.Storage.GitHub.Repo = v
	}
	if v := os.Getenv("VIBED_REGISTRY_ENABLED"); v != "" {
		cfg.Registry.Enabled, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("VIBED_REGISTRY_URL"); v != "" {
		cfg.Registry.URL = v
	}
	if v := os.Getenv("VIBED_STORE_BACKEND"); v != "" {
		cfg.Store.Backend = v
	}
	if v := os.Getenv("VIBED_STORE_SQLITE_PATH"); v != "" {
		cfg.Store.SQLite.Path = v
	}
	if v := os.Getenv("KUBECONFIG"); v != "" && cfg.Kubernetes.Kubeconfig == "" {
		cfg.Kubernetes.Kubeconfig = v
	}
	if v := os.Getenv("VIBED_KNATIVE_DOMAIN_SUFFIX"); v != "" {
		cfg.Knative.DomainSuffix = v
	}
	if v := os.Getenv("VIBED_KNATIVE_GATEWAY_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Knative.GatewayPort = n
		}
	}
	// Auth overrides
	if v := os.Getenv("VIBED_AUTH_ENABLED"); v != "" {
		cfg.Auth.Enabled, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("VIBED_AUTH_MODE"); v != "" {
		cfg.Auth.Mode = v
	}
	// Single API key via env var (appended to any YAML-configured keys)
	if v := os.Getenv("VIBED_AUTH_API_KEY"); v != "" {
		cfg.Auth.Enabled = true
		if cfg.Auth.Mode == "" {
			cfg.Auth.Mode = "apikey"
		}
		cfg.Auth.APIKeys = append(cfg.Auth.APIKeys, APIKeyConf{
			Key:  v,
			Name: "env-key",
		})
	}

	// OIDC overrides
	if v := os.Getenv("VIBED_AUTH_OIDC_ISSUER"); v != "" {
		cfg.Auth.OIDC.Issuer = v
	}
	if v := os.Getenv("VIBED_AUTH_OIDC_AUDIENCE"); v != "" {
		cfg.Auth.OIDC.Audience = v
	}
	if v := os.Getenv("VIBED_AUTH_OIDC_ADMIN_ROLE"); v != "" {
		cfg.Auth.OIDC.AdminRole = v
	}

	// TLS overrides
	if v := os.Getenv("VIBED_TLS_ENABLED"); v != "" {
		cfg.Auth.TLS.Enabled, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("VIBED_TLS_CERT_FILE"); v != "" {
		cfg.Auth.TLS.CertFile = v
	}
	if v := os.Getenv("VIBED_TLS_KEY_FILE"); v != "" {
		cfg.Auth.TLS.KeyFile = v
	}
	if v := os.Getenv("VIBED_TLS_AUTO"); v != "" {
		cfg.Auth.TLS.AutoTLS, _ = strconv.ParseBool(v)
	}

	// Limits overrides
	if v := os.Getenv("VIBED_LIMITS_MAX_TOTAL_FILE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Limits.MaxTotalFileSize = n
		}
	}
	if v := os.Getenv("VIBED_LIMITS_MAX_FILE_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Limits.MaxFileCount = n
		}
	}
	if v := os.Getenv("VIBED_LIMITS_MAX_LOG_LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Limits.MaxLogLines = n
		}
	}

	// GC overrides
	if v := os.Getenv("VIBED_GC_ENABLED"); v != "" {
		cfg.GC.Enabled, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("VIBED_GC_INTERVAL"); v != "" {
		cfg.GC.Interval = v
	}
	if v := os.Getenv("VIBED_GC_MAX_AGE"); v != "" {
		cfg.GC.MaxAge = v
	}
	if v := os.Getenv("VIBED_GC_DRY_RUN"); v != "" {
		cfg.GC.DryRun, _ = strconv.ParseBool(v)
	}

	// Tracing overrides (standard OTel env var takes precedence)
	if v := os.Getenv("VIBED_TRACING_ENABLED"); v != "" {
		cfg.Tracing.Enabled, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		cfg.Tracing.Endpoint = v
		cfg.Tracing.Enabled = true
	}
	if v := os.Getenv("VIBED_TRACING_ENDPOINT"); v != "" {
		cfg.Tracing.Endpoint = v
	}
	if v := os.Getenv("VIBED_TRACING_SAMPLE_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Tracing.SampleRate = f
		}
	}

	// Rate limit overrides
	if v := os.Getenv("VIBED_RATE_LIMIT_ENABLED"); v != "" {
		cfg.Server.RateLimit.Enabled, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("VIBED_RATE_LIMIT_RPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.Server.RateLimit.RequestsPerSecond = f
		}
	}
	if v := os.Getenv("VIBED_RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Server.RateLimit.Burst = n
		}
	}
}

func validate(cfg *Config) error {
	validTransports := map[string]bool{"stdio": true, "http": true, "both": true}
	if !validTransports[cfg.Server.Transport] {
		return fmt.Errorf("server.transport must be one of: stdio, http, both (got %q)", cfg.Server.Transport)
	}

	validLogFormats := map[string]bool{"text": true, "json": true}
	if !validLogFormats[cfg.Server.LogFormat] {
		return fmt.Errorf("server.logFormat must be 'text' or 'json' (got %q)", cfg.Server.LogFormat)
	}
	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[cfg.Server.LogLevel] {
		return fmt.Errorf("server.logLevel must be one of: debug, info, warn, error (got %q)", cfg.Server.LogLevel)
	}

	validTargets := map[string]bool{"auto": true, "knative": true, "kubernetes": true}
	if !validTargets[cfg.Deployment.PreferredTarget] {
		return fmt.Errorf("deployment.preferredTarget must be one of: auto, knative, kubernetes (got %q)", cfg.Deployment.PreferredTarget)
	}

	validStorageBackends := map[string]bool{"local": true, "github": true, "gitlab": true}
	if !validStorageBackends[cfg.Storage.Backend] {
		return fmt.Errorf("storage.backend must be one of: local, github, gitlab (got %q)", cfg.Storage.Backend)
	}

	if cfg.Storage.Backend == "github" {
		if cfg.Storage.GitHub.Owner == "" || cfg.Storage.GitHub.Repo == "" {
			return fmt.Errorf("storage.github.owner and storage.github.repo are required when storage.backend is github")
		}
	}

	if cfg.Storage.Backend == "gitlab" {
		if cfg.Storage.GitLab.ProjectID == 0 {
			return fmt.Errorf("storage.gitlab.projectID is required when storage.backend is gitlab")
		}
	}

	validStoreBackends := map[string]bool{"memory": true, "configmap": true, "sqlite": true}
	if !validStoreBackends[cfg.Store.Backend] {
		return fmt.Errorf("store.backend must be one of: memory, configmap, sqlite (got %q)", cfg.Store.Backend)
	}

	if cfg.Store.Backend == "sqlite" && cfg.Store.SQLite.Path == "" {
		return fmt.Errorf("store.sqlite.path is required when store.backend is sqlite")
	}

	validEngines := map[string]bool{"pack": true, "buildah": true}
	if !validEngines[cfg.Builder.Engine] {
		return fmt.Errorf("builder.engine must be one of: pack, buildah (got %q)", cfg.Builder.Engine)
	}

	if cfg.Builder.Engine == "buildah" && !cfg.Registry.Enabled {
		return fmt.Errorf("registry must be enabled when using buildah builder (buildah needs a registry to push images)")
	}

	if cfg.Registry.Enabled && cfg.Registry.URL == "" {
		return fmt.Errorf("registry.url is required when registry.enabled is true")
	}

	// Validate auth config
	if cfg.Auth.Enabled {
		validModes := map[string]bool{"apikey": true, "oauth": true, "oidc": true, "": true}
		if !validModes[cfg.Auth.Mode] {
			return fmt.Errorf("auth.mode must be one of: apikey, oauth, oidc (got %q)", cfg.Auth.Mode)
		}
		if (cfg.Auth.Mode == "apikey" || cfg.Auth.Mode == "") && len(cfg.Auth.APIKeys) == 0 {
			return fmt.Errorf("at least one API key is required when auth.mode is 'apikey'")
		}
		if cfg.Auth.Mode == "oidc" {
			if cfg.Auth.OIDC.Issuer == "" {
				return fmt.Errorf("auth.oidc.issuer is required when auth.mode is 'oidc'")
			}
			if cfg.Auth.OIDC.Audience == "" {
				return fmt.Errorf("auth.oidc.audience is required when auth.mode is 'oidc' (prevents cross-app token reuse)")
			}
		}
		validRoles := map[string]bool{"admin": true, "user": true, "": true}
		for _, key := range cfg.Auth.APIKeys {
			if !validRoles[key.Role] {
				return fmt.Errorf("auth.apiKeys[%q].role must be 'admin' or 'user' (got %q)", key.Name, key.Role)
			}
		}
	}

	// Validate TLS config
	if cfg.Auth.TLS.Enabled {
		hasCerts := cfg.Auth.TLS.CertFile != "" && cfg.Auth.TLS.KeyFile != ""
		if !hasCerts && !cfg.Auth.TLS.AutoTLS {
			return fmt.Errorf("TLS enabled but no certificate configured: set certFile/keyFile or enable autoTLS")
		}
	}

	// Validate GC config
	if cfg.GC.Enabled {
		if _, err := time.ParseDuration(cfg.GC.Interval); err != nil {
			return fmt.Errorf("gc.interval must be a valid duration (got %q): %w", cfg.GC.Interval, err)
		}
		if _, err := time.ParseDuration(cfg.GC.MaxAge); err != nil {
			return fmt.Errorf("gc.maxAge must be a valid duration (got %q): %w", cfg.GC.MaxAge, err)
		}
	}

	_ = strings.ToLower // suppress unused import if needed

	return nil
}
