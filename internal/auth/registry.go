package auth

import (
	"log/slog"
	"sort"
	"sync"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/store"
)

// ProviderFactory builds the mcpauth.TokenVerifier for one auth mode. It does
// its own config validation and returns a descriptive error (e.g. "no API keys
// configured"). userStore may be nil. Registered from init(): the OSS core
// registers apikey/oauth/oidc; a closed enterprise module registers additional
// modes (e.g. saml) the same way, reading any settings it needs from
// cfg.Options so no field has to be added to config.AuthConfig.
type ProviderFactory func(cfg config.AuthConfig, userStore store.UserStore, logger *slog.Logger) (mcpauth.TokenVerifier, error)

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
