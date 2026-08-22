---
sidebar_position: 2
---

# Custom Auth Providers

vibeD authenticates every request to its MCP, `/api`, `/v1`, and internal-source endpoints with a **bearer token verifier**. The core ships three modes (`apikey`, `oauth`, and `oidc`), but the verifier is pluggable: you can register your own **auth mode** (SAML SP, a bespoke JWT issuer, an internal token service) from a separate Go module without touching the core.

You do this through the public extension surface in [`pkg/plugin`](https://github.com/vibed-project/vibeD/tree/main/pkg/plugin). It re-exports the internal auth types as aliases, so a value that satisfies `plugin.TokenVerifier` also satisfies the internal interface, so no adapter is needed.

This page assumes you have read [Authentication & HTTPS](../configuration/authentication.md), which covers the built-in modes and config.

## What an auth mode contributes

An auth mode is a factory that returns a `Provider`:

```go
// package github.com/vibed-project/vibeD/pkg/plugin

type Provider struct {
	Verifier TokenVerifier // required: validates the bearer token on every request
	Routes   []Route       // optional: PUBLIC login routes mounted OUTSIDE auth
}
```

| Field      | Required | Purpose                                                                                   |
| ---------- | -------- | ----------------------------------------------------------------------------------------- |
| `Verifier` | yes      | Checked on **every** authenticated request. A `nil` verifier is a startup error.          |
| `Routes`   | no       | Public HTTP endpoints your login flow needs (e.g. an SSO metadata / callback endpoint).   |

A bearer-only mode (like the built-in `apikey`/`oauth`/`oidc`) leaves `Routes` empty.

### The verifier

`TokenVerifier` and `TokenInfo` are the MCP SDK types the core wraps in `auth.RequireBearerToken`:

```go
type TokenVerifier func(ctx context.Context, token string, req *http.Request) (*TokenInfo, error)
```

Return a `*TokenInfo` on success; its `UserID` becomes the authenticated identity for the rest of the request. Return `mcpauth.ErrInvalidToken` (from `github.com/modelcontextprotocol/go-sdk/auth`) on a bad token; the middleware turns that into a `401`.

`TokenInfo` carries at least:

| Field        | Meaning                                                             |
| ------------ | ------------------------------------------------------------------ |
| `UserID`     | Stable identity string. Drives ownership, role lookup, audit.      |
| `Scopes`     | Optional granted scopes.                                           |
| `Expiration` | When the token is no longer valid.                                 |

### The factory

`AuthProviderFactory` builds the `Provider` for one mode. It does its own config validation and returns a descriptive error:

```go
type AuthProviderFactory func(cfg AuthConfig, userStore UserStore, logger *slog.Logger) (*Provider, error)
```

- `cfg` is the whole `auth:` config section. Read your mode's settings from **`cfg.Options`** (a `map[string]string`); the core modes ignore it, so you never have to add a field to the config struct.
- `userStore` may be `nil`. When present you can auto-provision user records on first login and look users up.
- Return a descriptive error (e.g. `"metadata URL not configured"`) rather than panicking; the process fails to start with your message.

## Registering a mode

Register from an `init()` in your provider package:

```go
func RegisterAuthProvider(mode string, f AuthProviderFactory)
func AuthProviders() []string // registered mode names, sorted
```

`RegisterAuthProvider` **panics** on a `nil` factory or a duplicate mode; both are programmer errors surfaced at process start. The mode name is what operators put in `auth.mode`.

## Selecting your mode

vibeD picks the mode from the `auth.mode` config key and hands your factory the settings under `auth.options`:

```yaml
auth:
  enabled: true
  mode: "saml"            # your registered mode name
  options:
    metadataURL: "https://idp.example.com/metadata"
    acsPath: "/saml/acs"
    entityID: "https://vibed.example.com"
```

Your factory reads those with `cfg.Options["metadataURL"]`, etc.

:::warning Config validation currently allow-lists the built-in modes
`config.Validate()` rejects any `auth.mode` outside `apikey`, `oauth`, and `oidc`, so a freshly registered custom mode will fail validation before the registry is consulted. To ship a custom mode today you must widen that allow-list in your fork/build (or run with validation adjusted). The registry itself imposes no such restriction; an empty `auth.mode` defaults to `apikey`.
:::

## Public login routes

Bearer verification answers "is this request already authenticated?", but an SSO flow needs endpoints a user can reach **before** they have a session: an IdP-redirect, an assertion-consumer / callback endpoint, a metadata document. Those are `Provider.Routes`.

```go
type Route struct {
	Pattern string       // net/http ServeMux pattern, e.g. "POST /saml/acs"
	Handler http.Handler
}
```

Provider routes are mounted **outside** the bearer-auth middleware; they are how a user obtains a session in the first place, so they cannot themselves require one. Keep them on **public** paths, clear of the authenticated prefixes (`/api`, `/v1`, `/mcp`, `/internal/sources`).

## Suspended-user check

The core wraps your verifier with one extra check it enforces at auth time, so you do not implement it yourself:

- After your verifier returns a `UserID`, if a `userStore` is present the core loads that user and rejects the request with `401 account suspended` when `user.Status == "suspended"`.

Your verifier's job is just to authenticate the token and yield the right `UserID`; suspension, role resolution, and audit are layered on by the core.

## Skeleton provider

A minimal mode with a verifier and one public callback route:

```go
package myauth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/vibed-project/vibeD/pkg/plugin"
)

func init() {
	plugin.RegisterAuthProvider("mymode", newProvider)
}

func newProvider(cfg plugin.AuthConfig, users plugin.UserStore, logger *slog.Logger) (*plugin.Provider, error) {
	issuer := cfg.Options["issuer"]
	if issuer == "" {
		return nil, fmt.Errorf("auth.options.issuer is required for mode 'mymode'")
	}

	return &plugin.Provider{
		Verifier: verify(issuer, users, logger),
		Routes: []plugin.Route{
			// PUBLIC: mounted outside the bearer-auth middleware.
			{Pattern: "GET /mymode/callback", Handler: callback(users, logger)},
		},
	}, nil
}

func verify(issuer string, users plugin.UserStore, logger *slog.Logger) plugin.TokenVerifier {
	return func(ctx context.Context, token string, req *http.Request) (*plugin.TokenInfo, error) {
		userID, ok := validate(ctx, issuer, token) // your token check
		if !ok {
			logger.Debug("mymode: token rejected", "path", req.URL.Path)
			return nil, mcpauth.ErrInvalidToken // -> 401
		}
		// Optionally auto-provision on first login when users != nil.
		return &plugin.TokenInfo{UserID: userID}, nil
	}
}

func callback(users plugin.UserStore, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Complete the SSO handshake, mint/return a bearer token the client
		// then sends as `Authorization: Bearer ...` to authenticated endpoints.
		w.WriteHeader(http.StatusOK)
	})
}
```

## Wiring it into a binary

Go forbids importing another module's `internal/`, which is why `pkg/plugin` exists: it re-exports the registry interfaces as **type aliases**, so your out-of-tree type satisfies the core interface directly. To ship a custom binary, blank-import your provider package (its `init()` calls `RegisterAuthProvider`) and call the standard server entrypoint:

```go
package main

import (
	_ "example.com/vibed-providers/myauth" // init() registers "mymode"

	"github.com/vibed-project/vibeD/pkg/server"
)

func main() {
	server.Main()
}
```

Build it with the same toolchain settings as the core:

```bash
GOTOOLCHAIN=auto GO111MODULE=on go build ./...
```

Set `auth.mode: mymode` in your config and the server resolves your factory at startup.

## See also

- [Authentication & HTTPS](../configuration/authentication.md): the built-in `apikey`/`oauth`/`oidc` modes and TLS.
- [Configuration Reference](../configuration/config-reference.md): the full `auth:` config section.
