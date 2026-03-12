package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	vibedauth "github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
)

// UserStorageRouter implements the Storage interface and routes each storage
// operation to a per-user backend. Users without a per-user config fall back
// to the global default storage.
type UserStorageRouter struct {
	mu             sync.RWMutex
	users          map[string]Storage               // lazy-created per-user backends
	userConfigs    map[string]*config.UserStorageConf // from API key configs
	fallback       Storage                           // default for users without config
	localCacheBase string                             // base dir for per-user local caches
}

// NewUserStorageRouter creates a router that directs storage calls to per-user backends.
// Only API keys that have a Storage config will get per-user routing; all others
// use the fallback storage.
func NewUserStorageRouter(apiKeys []config.APIKeyConf, fallback Storage, localCacheBase string) *UserStorageRouter {
	userConfigs := make(map[string]*config.UserStorageConf)
	for i := range apiKeys {
		if apiKeys[i].Storage != nil {
			userConfigs[apiKeys[i].Name] = apiKeys[i].Storage
		}
	}

	return &UserStorageRouter{
		users:          make(map[string]Storage),
		userConfigs:    userConfigs,
		fallback:       fallback,
		localCacheBase: localCacheBase,
	}
}

// HasPerUserConfigs returns true if any API key has per-user storage configured.
func HasPerUserConfigs(apiKeys []config.APIKeyConf) bool {
	for _, k := range apiKeys {
		if k.Storage != nil {
			return true
		}
	}
	return false
}

func (r *UserStorageRouter) StoreSource(ctx context.Context, artifactID string, files map[string]string) (*StorageRef, error) {
	stg := r.resolve(ctx)
	return stg.StoreSource(ctx, artifactID, files)
}

func (r *UserStorageRouter) StoreManifest(ctx context.Context, artifactID string, manifests map[string][]byte) error {
	stg := r.resolve(ctx)
	return stg.StoreManifest(ctx, artifactID, manifests)
}

func (r *UserStorageRouter) GetSourcePath(ctx context.Context, artifactID string) (string, error) {
	stg := r.resolve(ctx)
	return stg.GetSourcePath(ctx, artifactID)
}

func (r *UserStorageRouter) Delete(ctx context.Context, artifactID string) error {
	stg := r.resolve(ctx)
	return stg.Delete(ctx, artifactID)
}

// resolve returns the storage backend for the current user.
// If the user has no per-user config or no user is in context, returns fallback.
func (r *UserStorageRouter) resolve(ctx context.Context) Storage {
	userID := vibedauth.UserIDFromContext(ctx)
	if userID == "" {
		return r.fallback
	}

	// Check if user has per-user config
	cfg, ok := r.userConfigs[userID]
	if !ok {
		return r.fallback
	}

	// Fast path: already created
	r.mu.RLock()
	stg, ok := r.users[userID]
	r.mu.RUnlock()
	if ok {
		return stg
	}

	// Slow path: create lazily with double-checked locking
	r.mu.Lock()
	defer r.mu.Unlock()

	// Re-check under write lock
	if stg, ok = r.users[userID]; ok {
		return stg
	}

	stg, err := r.createUserStorage(userID, cfg)
	if err != nil {
		// On error, fall back to default (don't crash)
		fmt.Printf("WARNING: failed to create per-user storage for %q: %v (falling back to default)\n", userID, err)
		return r.fallback
	}

	r.users[userID] = stg
	return stg
}

// createUserStorage creates a storage backend for a specific user.
func (r *UserStorageRouter) createUserStorage(userID string, cfg *config.UserStorageConf) (Storage, error) {
	localCacheDir := filepath.Join(r.localCacheBase, "users", userID)

	switch cfg.Backend {
	case "github":
		if cfg.GitHub == nil {
			return nil, fmt.Errorf("github config is required when backend is github")
		}
		branch := cfg.GitHub.Branch
		if branch == "" {
			branch = "main"
		}
		token, err := config.ResolveSecret(cfg.GitHub.Token)
		if err != nil {
			return nil, fmt.Errorf("resolving GitHub token: %w", err)
		}
		return NewGitHubStorage(cfg.GitHub.Owner, cfg.GitHub.Repo, branch, token, localCacheDir)

	case "gitlab":
		if cfg.GitLab == nil {
			return nil, fmt.Errorf("gitlab config is required when backend is gitlab")
		}
		url := cfg.GitLab.URL
		if url == "" {
			url = "https://gitlab.com"
		}
		branch := cfg.GitLab.Branch
		if branch == "" {
			branch = "main"
		}
		token, err := config.ResolveSecret(cfg.GitLab.Token)
		if err != nil {
			return nil, fmt.Errorf("resolving GitLab token: %w", err)
		}
		return NewGitLabStorage(url, cfg.GitLab.ProjectID, branch, token, localCacheDir)

	default:
		return nil, fmt.Errorf("unsupported per-user storage backend: %q", cfg.Backend)
	}
}
