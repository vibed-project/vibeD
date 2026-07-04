package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the complete vibeD configuration.
type Config struct {
	Organization OrganizationConfig `yaml:"organization"`
	Server       ServerConfig       `yaml:"server"`
	Auth         AuthConfig         `yaml:"auth"`
	Deployment   DeploymentConfig   `yaml:"deployment"`
	Storage      StorageConfig      `yaml:"storage"`
	Registry     RegistryConfig     `yaml:"registry"`
	Store        StoreConfig        `yaml:"store"`
	Kubernetes   KubernetesConfig   `yaml:"kubernetes"`
	Limits       LimitsConfig       `yaml:"limits"`
	Quotas       QuotasConfig       `yaml:"quotas"`
	Audit        AuditConfig        `yaml:"audit"`
	GC           GCConfig           `yaml:"gc"`
	Tracing      TracingConfig      `yaml:"tracing"`
}

// AuditConfig controls the governance audit trail. When FailClosed is true, a
// mutating action (deploy/delete/rollback/suspend) that vibeD cannot persist
// to the audit store is rejected — preferring availability loss over an
// untraceable change. Set false for development and clusters that prefer
// availability over compliance guarantees.
type AuditConfig struct {
	FailClosed bool `yaml:"failClosed"` // reject the action when the audit write fails
}

// QuotasConfig caps how many concurrent apps an owner / department may deploy
// (enterprise governance). Disabled by default; a new deploy is hard-gated
// when it would exceed either ceiling. Counts are over live VibedApps (by the
// vibed.dev/owner and vibed.dev/department labels); redeploys of an existing
// app don't count. A limit of 0 means "unlimited" for that axis.
type QuotasConfig struct {
	Enabled              bool           `yaml:"enabled"`
	MaxAppsPerOwner      int            `yaml:"maxAppsPerOwner"`      // per-user ceiling (0 = unlimited)
	MaxAppsPerDepartment int            `yaml:"maxAppsPerDepartment"` // per-department aggregate ceiling (0 = unlimited)
	PerDepartment        map[string]int `yaml:"perDepartment"`        // override the department ceiling by department name
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
	// MaxConcurrentLogStreamsPerUser caps simultaneous /v1/logs SSE streams a
	// single authenticated user may hold open. 0 = unlimited (legacy). A
	// modest default (10) blocks the trivial DoS where one user opens
	// hundreds of streams to exhaust controller memory.
	MaxConcurrentLogStreamsPerUser int `yaml:"maxConcurrentLogStreamsPerUser"`
}

// AuthConfig holds authentication and TLS settings.
type AuthConfig struct {
	Enabled bool         `yaml:"enabled"`
	Mode    string       `yaml:"mode"` // "apikey", "oauth", or "oidc" (+ modes registered by out-of-tree providers)
	APIKeys []APIKeyConf `yaml:"apiKeys"`
	OIDC    OIDCConfig   `yaml:"oidc"`
	TLS     TLSConf      `yaml:"tls"`
	// TrustedProxies is a list of CIDRs (e.g. "10.0.0.0/8") for the "oauth"
	// passthrough mode. The X-Forwarded-User identity header is trusted ONLY when
	// the request's source IP falls within one of these CIDRs. If empty, the
	// header is not trusted (every request maps to a generic identity), so a
	// direct client cannot spoof a user by setting the header. Ignored by the
	// apikey/oidc modes.
	TrustedProxies []string `yaml:"trustedProxies,omitempty"`
	// Options is a generic bag read by out-of-tree auth providers. Core providers
	// (apikey/oauth/oidc) ignore it, so a new mode needs no field added to this
	// struct.
	Options map[string]string `yaml:"options,omitempty"`
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
	URL       string `yaml:"url,omitempty"` // defaults "https://gitlab.com"
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
	PreferredTarget string `yaml:"preferredTarget"` // "auto" or "kubernetes"
	Namespace       string `yaml:"namespace"`
	// AppsNamespace is where the /v1 path creates VibedApp CRs. It must match
	// the namespace the warm pools (SandboxTemplate/SandboxWarmPool) live in,
	// because agent-sandbox requires a SandboxClaim to be co-located with its
	// SandboxTemplate. Defaults to "vibed-apps".
	AppsNamespace string        `yaml:"appsNamespace"`
	ReadyTimeout  time.Duration `yaml:"readyTimeout"` // how long deployers wait for a workload to become Ready before failing the deploy
}

type StorageConfig struct {
	Backend string             `yaml:"backend"` // "local", "github", or "gitlab"
	Local   LocalStorageConfig `yaml:"local"`
	GitHub  GitHubConfig       `yaml:"github"`
	GitLab  GitLabConfig       `yaml:"gitlab"`
	// Tarball configures the source-blob store for the /v1/deploy path
	// (separate from the file-tree Storage above, which serves the legacy
	// MCP build path).
	Tarball TarballConfig `yaml:"tarball"`
}

// TarballConfig selects how /v1/deploy persists the uploaded source tarball
// so vibed-agent can pull it. "served" (default) keeps the blob on vibeD's
// own volume and serves it over an authenticated in-cluster URL — no extra
// infra. "s3" streams to S3/MinIO and hands the agent a pre-signed URL.
type TarballConfig struct {
	Backend string              `yaml:"backend"` // "served" (default) | "s3"
	Served  ServedTarballConfig `yaml:"served"`
	S3      S3TarballConfig     `yaml:"s3"`
}

type ServedTarballConfig struct {
	// BasePath is the directory tarballs are written to (should be a PVC).
	BasePath string `yaml:"basePath"`
	// PublicBaseURL is the in-cluster base the agent dials, e.g.
	// "http://vibed.vibed-system.svc.cluster.local:8080". The store appends
	// /internal/sources/<id>.tar.gz.
	PublicBaseURL string `yaml:"publicBaseURL"`
}

type S3TarballConfig struct {
	Endpoint   string `yaml:"endpoint"` // empty for AWS; set for MinIO
	Bucket     string `yaml:"bucket"`
	Region     string `yaml:"region"`
	AccessKey  string `yaml:"accessKey"`
	SecretKey  string `yaml:"secretKey"`
	PresignTTL string `yaml:"presignTTL"` // GET URL validity (default "15m")
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
	// Options is a generic bag passed verbatim to the store backend factory.
	// Core backends ignore it; an out-of-tree backend reads its own settings from
	// here (a DSN, pool size, …) so adding a backend needs no change to this
	// struct.
	Options map[string]string `yaml:"options,omitempty"`
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
			AppsNamespace:   "vibed-apps",
			ReadyTimeout:    10 * time.Minute, // generous; cold image pulls + Sandbox reconcile can be slow
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
			Tarball: TarballConfig{
				Backend: "served",
				Served: ServedTarballConfig{
					BasePath: "/data/vibed/sources",
				},
				S3: S3TarballConfig{
					PresignTTL: "15m",
				},
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
		Limits: LimitsConfig{
			MaxTotalFileSize:               50 * 1024 * 1024, // 50 MB
			MaxFileCount:                   500,
			MaxLogLines:                    10000,
			MaxConcurrentLogStreamsPerUser: 10,
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
		Audit: AuditConfig{
			// Default fail-open: dev clusters and the default Helm install
			// don't have a persistent audit store wired, and we'd rather
			// degrade auditing than block deploys out of the box. Production
			// values.yaml flips this to true.
			FailClosed: false,
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

	validTargets := map[string]bool{"auto": true, "kubernetes": true}
	if !validTargets[cfg.Deployment.PreferredTarget] {
		return fmt.Errorf("deployment.preferredTarget must be one of: auto, kubernetes (got %q)", cfg.Deployment.PreferredTarget)
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

	if cfg.Registry.Enabled && cfg.Registry.URL == "" {
		return fmt.Errorf("registry.url is required when registry.enabled is true")
	}

	// Validate auth config
	if cfg.Auth.Enabled {
		validModes := map[string]bool{"apikey": true, "oauth": true, "oidc": true, "": true}
		if !validModes[cfg.Auth.Mode] {
			return fmt.Errorf("auth.mode must be one of: apikey, oauth, oidc (got %q)", cfg.Auth.Mode)
		}
		if (cfg.Auth.Mode == "apikey" || cfg.Auth.Mode == "oauth" || cfg.Auth.Mode == "") && len(cfg.Auth.APIKeys) == 0 {
			return fmt.Errorf("at least one API key (or proxy secret) is required when auth.mode is %q", cfg.Auth.Mode)
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

	return nil
}
