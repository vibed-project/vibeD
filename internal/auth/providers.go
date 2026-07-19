package auth

import (
	"fmt"
	"log/slog"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/store"
)

// Register the three built-in auth modes. Each carries the same validation the
// previous switch in Middleware performed. Additional modes can be registered by
// an out-of-tree module via RegisterProvider.
func init() {
	RegisterProvider("apikey", func(cfg config.AuthConfig, userStore store.UserStore, logger *slog.Logger) (*Provider, error) {
		if len(cfg.APIKeys) == 0 {
			return nil, fmt.Errorf("auth.mode is 'apikey' but no API keys are configured")
		}
		return &Provider{Verifier: apiKeyVerifier(resolveAPIKeys(cfg.APIKeys, logger), userStore, logger)}, nil
	})

	RegisterProvider("oauth", func(cfg config.AuthConfig, _ store.UserStore, logger *slog.Logger) (*Provider, error) {
		if len(cfg.APIKeys) == 0 {
			return nil, fmt.Errorf("auth.mode is 'oauth' but no API keys (proxy secrets) are configured")
		}
		trusted := parseTrustedProxies(cfg.TrustedProxies, logger)
		if len(trusted) == 0 {
			logger.Warn("auth.mode 'oauth' has no auth.trustedProxies configured: X-Forwarded-User will be ignored and all requests map to a single 'oauth-user' identity. Set auth.trustedProxies to the proxy CIDR(s) to enable per-user identity.")
		}
		return &Provider{Verifier: oauthPassthroughVerifier(resolveAPIKeys(cfg.APIKeys, logger), trusted, logger)}, nil
	})

	RegisterProvider("oidc", func(cfg config.AuthConfig, userStore store.UserStore, logger *slog.Logger) (*Provider, error) {
		v, err := newOIDCVerifier(cfg.OIDC, userStore, logger)
		if err != nil {
			return nil, fmt.Errorf("initializing OIDC verifier: %w", err)
		}
		return &Provider{Verifier: v}, nil
	})
}
