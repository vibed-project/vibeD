// Package plugin is the public extension surface of vibeD.
//
// It exists so a SEPARATE Go module — for example a closed-source enterprise
// edition — can plug its own implementations into the OSS core without importing
// the core's internal/ packages, which Go forbids across module boundaries.
//
// The package re-exports, as type aliases, the interfaces and helper types an
// out-of-tree provider must name, plus thin Register* wrappers over the internal
// registries. Because the exported names are aliases (not new types), a value
// that satisfies plugin.ArtifactStore also satisfies internal/store.ArtifactStore
// — they are the same type — so registration needs no adapter.
//
// Typical enterprise use:
//
//	func init() {
//	    plugin.RegisterStoreBackend("postgres", func(d plugin.StoreDeps) (plugin.ArtifactStore, error) {
//	        return newPostgresStore(d.Options["dsn"])
//	    })
//	    plugin.RegisterAuthProvider("saml", newSAMLProvider)
//	}
//
// The enterprise binary then imports pkg/server and calls server.Run; the
// registered backend/mode is selected by the usual store.backend / auth.mode
// config value. Error sentinels a store returns (api.ErrNotFound,
// api.ErrAlreadyExists, api.ErrVersionNotFound, …) already live in the public
// pkg/api package.
package plugin

import (
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/vibed-project/vibeD/internal/auth"
	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/store"
)

// --- Store backends ---------------------------------------------------------

// StoreDeps is what a store backend factory receives: the selected backend name,
// the SQLite path, the ConfigMap coordinates, a Kubernetes client, a generic
// Options bag (read your DSN etc. from here), and a logger.
type StoreDeps = store.Deps

// StoreBackendFactory builds a store from StoreDeps. The returned ArtifactStore
// may additionally implement UserStore, AuditStore, ShareLinkStore, and
// io.Closer; the core feature-detects those via type assertion.
type StoreBackendFactory = store.Factory

// Store interfaces and their supporting types. An out-of-tree backend implements
// ArtifactStore (and optionally the others). The api.* types in these method
// sets are already public in pkg/api.
type (
	ArtifactStore  = store.ArtifactStore
	UserStore      = store.UserStore
	AuditStore     = store.AuditStore
	ShareLinkStore = store.ShareLinkStore
	ListOptions    = store.ListOptions
	ListResult     = store.ListResult
	AuditQuery     = store.AuditQuery
)

// RegisterStoreBackend registers a store backend under name (e.g. "postgres").
// Call it from an init(). It panics on a nil factory or a duplicate name.
func RegisterStoreBackend(name string, f StoreBackendFactory) { store.Register(name, f) }

// StoreBackends returns the registered store backend names, sorted.
func StoreBackends() []string { return store.Backends() }

// --- Auth providers ---------------------------------------------------------

// AuthConfig is the auth section of the vibeD config. Out-of-tree providers
// typically read AuthConfig.Options for their settings (e.g. a SAML metadata
// URL) so a new mode needs no new config field.
type AuthConfig = config.AuthConfig

// TokenVerifier and TokenInfo are the MCP SDK types an auth provider produces:
// a TokenVerifier validates a bearer token and returns a TokenInfo (UserID,
// scopes, expiry).
type (
	TokenVerifier = mcpauth.TokenVerifier
	TokenInfo     = mcpauth.TokenInfo
)

// AuthProviderFactory builds the TokenVerifier for one auth mode. It does its
// own config validation and returns a descriptive error.
type AuthProviderFactory = auth.ProviderFactory

// RegisterAuthProvider registers an auth mode under name (e.g. "saml"). Call it
// from an init(). It panics on a nil factory or a duplicate mode.
func RegisterAuthProvider(mode string, f AuthProviderFactory) { auth.RegisterProvider(mode, f) }

// AuthProviders returns the registered auth mode names, sorted.
func AuthProviders() []string { return auth.Providers() }
