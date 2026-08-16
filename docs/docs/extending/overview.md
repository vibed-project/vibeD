---
sidebar_position: 1
---

# Extending vibeD

vibeD's control plane is assembled from **registries**. Storage, authentication, tenancy, deploy-time policy, usage metering, and secret schemes are all looked up at startup by name, so you can supply your own implementation of any of them from a **separate Go module** — no fork, no patch, no rebuild of the core.

The seam is the public [`pkg/plugin`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/plugin) package plus the reusable [`pkg/server`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/server) entry point. You blank-import your provider package into a tiny `main.go`, call `server.Main()`, and select your provider through the same `vibed.yaml` config keys everyone else uses.

## Why `pkg/plugin` exists

Go forbids importing another module's `internal/` packages. vibeD keeps its registry interfaces (`ArtifactStore`, `TokenVerifier`, `TenantResolver`, and friends) under `internal/`, so an out-of-tree module cannot name them directly.

`pkg/plugin` bridges that gap. It re-exports every interface and helper type an out-of-tree provider must name as a **type alias**, and wraps each internal registry with a thin `Register…` function:

```go
// in pkg/plugin
type ArtifactStore = store.ArtifactStore // alias, not a new type
```

Because these are aliases and not new types, a value in your module that satisfies `plugin.ArtifactStore` **is** the same type as `internal/store.ArtifactStore` — there is no adapter, no wrapper, and no reflection. Registration is a plain function call.

## The registration pattern

Every extension point follows the same three-step shape:

1. Your provider package's `init()` calls the matching `plugin.Register…` function.
2. Your binary's `main.go` blank-imports that package (`import _ "…/myprovider"`), which runs its `init()`.
3. `main()` calls `server.Main()`, and vibeD selects your provider by the ordinary config key (e.g. `store.backend`, `auth.mode`).

```go
package myprovider

import "github.com/vibed-project/vibeD/pkg/plugin"

func init() {
    plugin.RegisterStoreBackend("postgres", func(d plugin.StoreDeps) (plugin.ArtifactStore, error) {
        return newPostgresStore(d.Options["dsn"])
    })
}
```

`RegisterStoreBackend`, `RegisterAuthProvider`, and `RegisterSecretScheme` are keyed by a **name** and panic on a duplicate — you may register several. `RegisterTenantResolver`, `RegisterPolicyGate`, and `RegisterMeterSink` are **process-wide singletons** — the last registration wins, and the core installs a sensible default (single tenant, no policy gate, Prometheus meter) when none is registered.

## `pkg/server.Main` — the reusable entry point

`cmd/vibed` is a three-line `main()` that just calls `server.Main()`. All wiring — tracing, Kubernetes clients, storage, the artifact store, the deploy service, MCP, auth, and the HTTP server — lives in `pkg/server`, so a second binary never has to fork `main()`.

`server.Main()` parses the `-config` and `-transport` flags, loads and validates the config, builds the logger, and runs the server. Two lower-level helpers are exported for custom bootstraps that need to do work before serving:

| Function | Purpose |
| --- | --- |
| `server.Main()` | Standard entry point: parse flags → load config → run. Call this from `main()`. |
| `server.Run(cfg, logger)` | Wire every subsystem from an already-loaded config and serve until SIGINT/SIGTERM. |
| `server.LoadConfig(path)` | Load and validate a `vibed.yaml` (empty path = built-in defaults). |
| `server.NewLogger(cfg)` | Build the configured `slog.Logger` from the server config. |

## Extension points

Each row is one registry. Register from your provider's `init()`; select it with the listed config key. All identifiers live in `github.com/vibed-project/vibeD/pkg/plugin`.

| Extension point | Register function | Selected by | Multiplicity |
| --- | --- | --- | --- |
| Store backend | `RegisterStoreBackend(name, factory)` | `store.backend` | Named; many |
| Auth provider | `RegisterAuthProvider(mode, factory)` | `auth.mode` | Named; many |
| Secret scheme | `RegisterSecretScheme(scheme, resolver)` | `<scheme>:` prefix in any secret value | Named; many |
| Tenancy | `RegisterTenantResolver(factory)` | Registration presence (default: single tenant) | Singleton |
| Deploy-time policy | `RegisterPolicyGate(factory)` | Registration presence (default: no gate) | Singleton |
| Usage metering | `RegisterMeterSink(factory)` | Registration presence (default: Prometheus) | Singleton |
| Feature gating | `SetEntitlements(e)` / `RequireFeature(name)` | Editions flag (default: all features on) | Singleton |

A few notes on selection:

- **Store backends** are chosen by the `store.backend` config value. A backend that also implements `UserStore`, `AuditStore`, or `ShareLinkStore` gets those capabilities feature-detected via type assertion — implement only what you need.
- **Auth providers** are chosen by `auth.mode`; the core ships `apikey` and `oidc`. A provider may contribute public HTTP `Route`s (e.g. a SAML SP's metadata/ACS endpoints) that are mounted outside the bearer-auth middleware.
- **Secret schemes** resolve `"<scheme>:<ref>"` config values through `config.ResolveSecret`. The core handles `env:` and `file:`; register `vault:`, `kms:`, etc. yourself.
- **Feature gating** is an editions/feature-flag seam. The default edition enables **all** features, so a plain build serves everything; an out-of-tree build may call `SetEntitlements` once at startup to select a different edition, and provider registrations can call `RequireFeature` to gate their own activation.

## A minimal custom `main.go`

This is a complete out-of-tree binary. It contributes a Postgres store backend and then defers entirely to vibeD's wiring:

```go
package main

import (
    "github.com/vibed-project/vibeD/pkg/server"

    // Blank import runs the provider's init(), which calls
    // plugin.RegisterStoreBackend("postgres", …).
    _ "github.com/example/vibed-postgres"
)

func main() {
    server.Main()
}
```

Build and run it exactly like the stock binary:

```bash
GOTOOLCHAIN=auto GO111MODULE=on go build -o bin/vibed ./cmd/vibed
./bin/vibed -config /etc/vibed/vibed.yaml
```

Then select the backend in `vibed.yaml`:

```yaml
store:
  backend: "postgres"
  options:
    dsn: "postgres://vibed:secret@db:5432/vibed?sslmode=require"
```

Your factory reads its settings from `plugin.StoreDeps.Options` (here, `d.Options["dsn"]`), so adding a backend needs **no new config field** in the core.

## Per-seam guides

Each extension point has its own page with the exact interfaces, the `Deps` bag its factory receives, and a worked example:

- [Store backends](store-backends.md) — `ArtifactStore` and the optional `UserStore` / `AuditStore` / `ShareLinkStore` capabilities.
- [Auth providers](auth-providers.md) — `TokenVerifier`, `TokenInfo`, and login `Route`s.
- [Tenancy](tenancy.md) — resolving a request to a `Tenant` with its namespace and limits.
- [Policy & metering](policy-and-metering.md) — the deploy-time `PolicyGate` and usage `MeterSink`.
- [Secrets & features](secrets-and-features.md) — secret scheme resolvers and the entitlements seam.

## See also

- [Configuration reference](../configuration/config-reference.md) — the `vibed.yaml` keys your providers are selected by.
- [Architecture](../concepts/architecture.md) — where these seams sit in the control plane.
