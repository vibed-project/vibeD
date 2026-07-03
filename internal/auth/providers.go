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
		return &Provider{Verifier: apiKeyVerifier(cfg.APIKeys, userStore, logger)}, nil
	})

	RegisterProvider("oauth", func(cfg config.AuthConfig, _ store.UserStore, logger *slog.Logger) (*Provider, error) {
		if len(cfg.APIKeys) == 0 {
			return nil, fmt.Errorf("auth.mode is 'oauth' but no API keys (proxy secrets) are configured")
		}
		return &Provider{Verifier: oauthPassthroughVerifier(cfg.APIKeys, logger)}, nil
	})

	RegisterProvider("oidc", func(cfg config.AuthConfig, userStore store.UserStore, logger *slog.Logger) (*Provider, error) {
		v, err := newOIDCVerifier(cfg.OIDC, userStore, logger)
		if err != nil {
			return nil, fmt.Errorf("initializing OIDC verifier: %w", err)
		}
		return &Provider{Verifier: v}, nil
	})
}
