package plugin_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/vibed-project/vibeD/pkg/api"
	"github.com/vibed-project/vibeD/pkg/plugin"
)

// extStore mimics an out-of-tree backend: it implements plugin.ArtifactStore
// (an alias of the internal interface) using only public packages — the same
// constraint a separate enterprise module operates under.
type extStore struct{}

func (extStore) Create(context.Context, *api.Artifact) error              { return nil }
func (extStore) Get(context.Context, string) (*api.Artifact, error)       { return nil, &api.ErrNotFound{} }
func (extStore) GetByName(context.Context, string) (*api.Artifact, error) { return nil, nil }
func (extStore) List(context.Context, plugin.ListOptions) (*plugin.ListResult, error) {
	return &plugin.ListResult{}, nil
}
func (extStore) Update(context.Context, *api.Artifact) error { return nil }
func (extStore) Delete(context.Context, string) error        { return nil }
func (extStore) CreateVersion(context.Context, *api.ArtifactVersion) error {
	return nil
}
func (extStore) ListVersions(context.Context, string) ([]api.ArtifactVersion, error) {
	return nil, nil
}
func (extStore) GetVersion(context.Context, string, int) (*api.ArtifactVersion, error) {
	return nil, nil
}

func TestRegisterStoreBackend(t *testing.T) {
	plugin.RegisterStoreBackend("plugin-test-store", func(plugin.StoreDeps) (plugin.ArtifactStore, error) {
		return extStore{}, nil
	})
	if !contains(plugin.StoreBackends(), "plugin-test-store") {
		t.Fatalf("backend not registered; StoreBackends() = %v", plugin.StoreBackends())
	}
}

func TestRegisterAuthProvider(t *testing.T) {
	plugin.RegisterAuthProvider("plugin-test-auth", func(plugin.AuthConfig, plugin.UserStore, *slog.Logger) (*plugin.Provider, error) {
		return &plugin.Provider{
			Verifier: func(context.Context, string, *http.Request) (*plugin.TokenInfo, error) {
				return &plugin.TokenInfo{UserID: "ext"}, nil
			},
			Routes: []plugin.Route{{Pattern: "GET /plugin-test/metadata", Handler: http.NotFoundHandler()}},
		}, nil
	})
	if !contains(plugin.AuthProviders(), "plugin-test-auth") {
		t.Fatalf("auth mode not registered; AuthProviders() = %v", plugin.AuthProviders())
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
