package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the complete vibeD configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Auth       AuthConfig       `yaml:"auth"`
	Deployment DeploymentConfig `yaml:"deployment"`
	Builder    BuilderConfig    `yaml:"builder"`
	Storage    StorageConfig    `yaml:"storage"`
	Registry   RegistryConfig   `yaml:"registry"`
	Store      StoreConfig      `yaml:"store"`
	Kubernetes KubernetesConfig `yaml:"kubernetes"`
	Knative    KnativeConfig    `yaml:"knative"`
}

// AuthConfig holds authentication and TLS settings.
type AuthConfig struct {
	Enabled bool         `yaml:"enabled"`
	Mode    string       `yaml:"mode"` // "apikey" or "oauth"
	APIKeys []APIKeyConf `yaml:"apiKeys"`
	TLS     TLSConf      `yaml:"tls"`
}

// APIKeyConf represents a configured API key.
type APIKeyConf struct {
	Key    string   `yaml:"key"`    // Token value or "env:VAR_NAME"
	Name   string   `yaml:"name"`   // Human-readable name
	Scopes []string `yaml:"scopes"` // Allowed scopes (empty = all)
}

// TLSConf holds TLS certificate configuration.
type TLSConf struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"certFile"` // Path to TLS certificate
	KeyFile  string `yaml:"keyFile"`  // Path to TLS private key
	AutoTLS  bool   `yaml:"autoTLS"`  // Auto-generate self-signed cert for dev
}

type ServerConfig struct {
	Transport string `yaml:"transport"` // "stdio", "http", or "both"
	HTTPAddr  string `yaml:"httpAddr"`
}

type DeploymentConfig struct {
	PreferredTarget string `yaml:"preferredTarget"` // "auto", "knative", "kubernetes", "wasmcloud"
	Namespace       string `yaml:"namespace"`
}

type BuilderConfig struct {
	Image            string `yaml:"image"`
	RunImage         string `yaml:"runImage"`
	PullPolicy       string `yaml:"pullPolicy"`
	ContainerRuntime string `yaml:"containerRuntime"` // "auto", "docker", "podman"
}

type StorageConfig struct {
	Backend string             `yaml:"backend"` // "local" or "github"
	Local   LocalStorageConfig `yaml:"local"`
	GitHub  GitHubConfig       `yaml:"github"`
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

type RegistryConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
}

type StoreConfig struct {
	Backend   string          `yaml:"backend"` // "memory" or "configmap"
	ConfigMap ConfigMapConfig `yaml:"configmap"`
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
}

// Default returns a Config with sensible defaults.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Transport: "stdio",
			HTTPAddr:  ":8080",
		},
		Deployment: DeploymentConfig{
			PreferredTarget: "auto",
			Namespace:       "default",
		},
		Builder: BuilderConfig{
			Image:            "paketobuildpacks/builder-jammy-base:latest",
			PullPolicy:       "if-not-present",
			ContainerRuntime: "auto",
		},
		Storage: StorageConfig{
			Backend: "local",
			Local: LocalStorageConfig{
				BasePath: "/data/vibed/artifacts",
			},
			GitHub: GitHubConfig{
				Branch: "main",
			},
		},
		Registry: RegistryConfig{
			Enabled: false,
		},
		Store: StoreConfig{
			Backend: "memory",
			ConfigMap: ConfigMapConfig{
				Name:      "vibed-artifacts",
				Namespace: "vibed-system",
			},
		},
		Knative: KnativeConfig{
			DomainSuffix: "127.0.0.1.sslip.io",
			IngressClass: "kourier.ingress.networking.knative.dev",
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
	if v := os.Getenv("VIBED_SERVER_TRANSPORT"); v != "" {
		cfg.Server.Transport = v
	}
	if v := os.Getenv("VIBED_SERVER_HTTP_ADDR"); v != "" {
		cfg.Server.HTTPAddr = v
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
	if v := os.Getenv("VIBED_BUILDER_CONTAINER_RUNTIME"); v != "" {
		cfg.Builder.ContainerRuntime = v
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
	if v := os.Getenv("KUBECONFIG"); v != "" && cfg.Kubernetes.Kubeconfig == "" {
		cfg.Kubernetes.Kubeconfig = v
	}
	if v := os.Getenv("VIBED_KNATIVE_DOMAIN_SUFFIX"); v != "" {
		cfg.Knative.DomainSuffix = v
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
}

func validate(cfg *Config) error {
	validTransports := map[string]bool{"stdio": true, "http": true, "both": true}
	if !validTransports[cfg.Server.Transport] {
		return fmt.Errorf("server.transport must be one of: stdio, http, both (got %q)", cfg.Server.Transport)
	}

	validTargets := map[string]bool{"auto": true, "knative": true, "kubernetes": true, "wasmcloud": true}
	if !validTargets[cfg.Deployment.PreferredTarget] {
		return fmt.Errorf("deployment.preferredTarget must be one of: auto, knative, kubernetes, wasmcloud (got %q)", cfg.Deployment.PreferredTarget)
	}

	validStorageBackends := map[string]bool{"local": true, "github": true}
	if !validStorageBackends[cfg.Storage.Backend] {
		return fmt.Errorf("storage.backend must be one of: local, github (got %q)", cfg.Storage.Backend)
	}

	if cfg.Storage.Backend == "github" {
		if cfg.Storage.GitHub.Owner == "" || cfg.Storage.GitHub.Repo == "" {
			return fmt.Errorf("storage.github.owner and storage.github.repo are required when storage.backend is github")
		}
	}

	validStoreBackends := map[string]bool{"memory": true, "configmap": true}
	if !validStoreBackends[cfg.Store.Backend] {
		return fmt.Errorf("store.backend must be one of: memory, configmap (got %q)", cfg.Store.Backend)
	}

	if cfg.Registry.Enabled && cfg.Registry.URL == "" {
		return fmt.Errorf("registry.url is required when registry.enabled is true")
	}

	// Validate auth config
	if cfg.Auth.Enabled {
		validModes := map[string]bool{"apikey": true, "oauth": true, "": true}
		if !validModes[cfg.Auth.Mode] {
			return fmt.Errorf("auth.mode must be one of: apikey, oauth (got %q)", cfg.Auth.Mode)
		}
		if (cfg.Auth.Mode == "apikey" || cfg.Auth.Mode == "") && len(cfg.Auth.APIKeys) == 0 {
			return fmt.Errorf("at least one API key is required when auth.mode is 'apikey'")
		}
	}

	// Validate TLS config
	if cfg.Auth.TLS.Enabled {
		hasCerts := cfg.Auth.TLS.CertFile != "" && cfg.Auth.TLS.KeyFile != ""
		if !hasCerts && !cfg.Auth.TLS.AutoTLS {
			return fmt.Errorf("TLS enabled but no certificate configured: set certFile/keyFile or enable autoTLS")
		}
	}

	_ = strings.ToLower // suppress unused import if needed

	return nil
}
