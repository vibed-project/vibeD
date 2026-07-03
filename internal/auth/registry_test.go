package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/store"
)

// The three OSS built-in modes must self-register via init().
func TestProviders_BuiltinsRegistered(t *testing.T) {
	got := strings.Join(Providers(), ",")
	for _, want := range []string{"apikey", "oauth", "oidc"} {
		if !strings.Contains(got, want) {
			t.Errorf("auth mode %q not registered; Providers() = %s", want, got)
		}
	}
}

// An empty mode resolves to apikey (the historical default).
func TestLookupProvider_EmptyDefaultsToApikey(t *testing.T) {
	if _, ok := lookupProvider(""); !ok {
		t.Fatal(`lookupProvider("") should resolve to the apikey provider`)
	}
	if _, ok := lookupProvider("nope"); ok {
		t.Fatal(`lookupProvider("nope") should not resolve`)
	}
}

// Middleware must reject an unknown mode and accept a disabled config.
func TestMiddleware_UnknownModeAndDisabled(t *testing.T) {
	log := slog.Default()

	if _, err := Middleware(config.AuthConfig{Enabled: true, Mode: "nope"}, nil, log); err == nil {
		t.Error("Middleware with unknown mode should error")
	}
	if _, err := Middleware(config.AuthConfig{Enabled: false}, nil, log); err != nil {
		t.Errorf("Middleware with auth disabled should not error, got %v", err)
	}
}

// The apikey provider preserves the old switch's "no keys configured" guard.
func TestMiddleware_ApikeyRequiresKeys(t *testing.T) {
	log := slog.Default()

	if _, err := Middleware(config.AuthConfig{Enabled: true, Mode: "apikey"}, nil, log); err == nil {
		t.Error("apikey mode with no keys should error")
	}
	_, err := Middleware(config.AuthConfig{
		Enabled: true,
		Mode:    "apikey",
		APIKeys: []config.APIKeyConf{{Key: "secret", Name: "ci"}},
	}, nil, log)
	if err != nil {
		t.Errorf("apikey mode with a key should succeed, got %v", err)
	}
}

// A closed enterprise module registers a new mode exactly this way.
func TestRegisterProvider_Extensibility(t *testing.T) {
	RegisterProvider("test-saml", func(config.AuthConfig, store.UserStore, *slog.Logger) (*Provider, error) {
		return nil, nil
	})
	if _, ok := lookupProvider("test-saml"); !ok {
		t.Error("newly registered mode should resolve")
	}

	defer func() {
		if recover() == nil {
			t.Error("duplicate RegisterProvider should panic")
		}
	}()
	RegisterProvider("test-saml", func(config.AuthConfig, store.UserStore, *slog.Logger) (*Provider, error) {
		return nil, nil
	})
}

// A provider that contributes public login routes (like a SAML SP) has them
// surfaced by Build so the server can mount them outside the auth middleware.
func TestBuild_SurfacesProviderRoutes(t *testing.T) {
	RegisterProvider("test-routes", func(config.AuthConfig, store.UserStore, *slog.Logger) (*Provider, error) {
		return &Provider{
			Verifier: func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) { return nil, nil },
			Routes:   []Route{{Pattern: "GET /test/metadata", Handler: http.NotFoundHandler()}},
		}, nil
	})

	mw, routes, err := Build(config.AuthConfig{Enabled: true, Mode: "test-routes"}, nil, slog.Default())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if mw == nil {
		t.Error("Build returned a nil middleware")
	}
	if len(routes) != 1 || routes[0].Pattern != "GET /test/metadata" {
		t.Fatalf("routes = %+v, want one GET /test/metadata", routes)
	}
}
