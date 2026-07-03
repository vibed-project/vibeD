package auth

import (
	"log/slog"
	"net/http"
	"sort"
	"sync"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/store"
)

// Route is a public HTTP endpoint an auth provider serves as part of its login
// flow — e.g. a SAML SP's metadata, IdP-redirect, and assertion-consumer (ACS)
// endpoints. Provider routes are mounted OUTSIDE the bearer-auth middleware:
// they are how a user obtains a session in the first place, so they cannot
// themselves require a session. Pattern is a net/http ServeMux pattern
// ("GET /saml/metadata", "POST /saml/acs"). Keep provider routes on public
// paths (not under /api, /v1, /mcp, or /internal/sources).
type Route struct {
	Pattern string
	Handler http.Handler
}

// Provider is what one auth mode contributes: a required token Verifier (checked
// on every authenticated request) and, optionally, the public Routes its login
// flow needs. Bearer-only modes (apikey/oauth/oidc) leave Routes empty.
type Provider struct {
	Verifier mcpauth.TokenVerifier
	Routes   []Route
}

// ProviderFactory builds the Provider for one auth mode. It does its own config
// validation and returns a descriptive error (e.g. "no API keys configured").
// userStore may be nil. Registered from init(): the core registers
// apikey/oauth/oidc; an out-of-tree module can register additional modes the
// same way, reading any settings it needs from cfg.Options so no field has to be
// added to config.AuthConfig.
type ProviderFactory func(cfg config.AuthConfig, userStore store.UserStore, logger *slog.Logger) (*Provider, error)

var (
	providerMu       sync.RWMutex
	providerRegistry = map[string]ProviderFactory{}
)

// RegisterProvider adds an auth-mode provider. It panics on a nil factory or a
// duplicate mode — both are programmer errors surfaced at process start.
func RegisterProvider(mode string, f ProviderFactory) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if f == nil {
		panic("auth: RegisterProvider factory is nil for mode " + mode)
	}
	if _, dup := providerRegistry[mode]; dup {
		panic("auth: RegisterProvider called twice for mode " + mode)
	}
	providerRegistry[mode] = f
}

// lookupProvider resolves a mode to its factory. An empty mode is treated as
// "apikey", preserving the historical default.
func lookupProvider(mode string) (ProviderFactory, bool) {
	if mode == "" {
		mode = "apikey"
	}
	providerMu.RLock()
	defer providerMu.RUnlock()
	f, ok := providerRegistry[mode]
	return f, ok
}

// Providers returns the registered auth modes, sorted — for validation and
// error messages.
func Providers() []string {
	providerMu.RLock()
	defer providerMu.RUnlock()
	out := make([]string, 0, len(providerRegistry))
	for m := range providerRegistry {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
