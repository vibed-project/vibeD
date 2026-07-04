---
sidebar_position: 3
---

# Tenancy

By default vibeD runs a **single implicit tenant**: every request maps to one tenant whose namespace is the server's configured apps namespace, with no per-tenant ceilings. Behaviour is identical to a build that has never heard of tenancy — you only interact with this seam if you install your own resolver.

Multi-tenancy is an ordinary extensibility point. An out-of-tree Go module registers a `TenantResolver` that maps each request to a `Tenant` (a namespace plus limits); the deploy path then scopes every app to that namespace, labels it, and enforces the tenant's ceiling. Nothing about this seam is edition- or license-bound — the default resolver ships in the core and enables every code path.

## The `Tenant` type

A tenant is the isolation scope a request runs in. The exported type lives in [`pkg/plugin`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/plugin) as a type alias for the internal type, so a value your module builds satisfies the core interface with no adapter:

```go
type Tenant struct {
    ID        string       // stable tenant identifier ("" = the single-tenant default)
    Namespace string       // Kubernetes namespace this tenant's VibedApps live in
    Limits    TenantLimits // per-tenant resource ceilings
}

type TenantLimits struct {
    MaxApps int // cap on total live apps in the tenant (0 = unlimited)
}
```

| Field       | Meaning                                                                                     |
| ----------- | ------------------------------------------------------------------------------------------- |
| `ID`        | Identifies the tenant. Empty means the single-tenant default (inherits the server namespace, no ceilings). |
| `Namespace` | Where this tenant's `VibedApp` CRs are created. A **named** tenant must set this (see below). |
| `Limits.MaxApps` | Total live apps allowed in the tenant. `0` disables the cap.                            |

## The default: `SingleTenant`

When no resolver is registered, the core builds a `SingleTenant` resolver over the server's default tenant — one implicit tenant (empty `ID`) mapped to the configured apps namespace, with no limits:

```go
// what the core does when you register nothing
resolver := plugin.SingleTenant(plugin.Tenant{Namespace: "vibed-apps"})
```

Because the default tenant's `ID` is empty, the deploy path treats it as the single-tenant case: apps land in the server namespace and carry no `vibed.dev/tenant` label. Installing a resolver is purely additive — existing single-tenant apps are unchanged.

## Registering a resolver

An out-of-tree module enables multi-tenancy by registering exactly one resolver factory from its package `init()`:

```go
func RegisterTenantResolver(f func(TenantDeps) (TenantResolver, error))
```

`TenantResolver` is a one-method interface — you read whatever the request carries (a header, an OIDC claim, a bearer token — all reachable from the `context.Context`) and return the matching `Tenant`:

```go
type TenantResolver interface {
    Resolve(ctx context.Context) (Tenant, error)
}
```

The factory receives `TenantDeps`, which carries the core's default tenant (the namespace it would otherwise use) and a generic options bag your module reads its own settings from — so enabling multi-tenancy needs no new core config field:

```go
type TenantDeps struct {
    Default Tenant            // the single-tenant scope the core would use
    Options map[string]string // your module's settings
}
```

Registration is process-wide and singular: a second `RegisterTenantResolver` call panics.

## Skeleton resolver

A complete out-of-tree module is a small package plus a `main` that blank-imports it and calls `server.Main()`. The `init()` does the registration; `server.Main()` builds the resolver, loads config, and runs the server:

```go
// package yourorg/vibed-tenancy/tenant
package tenant

import (
    "context"
    "fmt"

    "github.com/vibed-project/vibeD/pkg/plugin"
)

func init() {
    plugin.RegisterTenantResolver(func(deps plugin.TenantDeps) (plugin.TenantResolver, error) {
        return &headerResolver{fallback: deps.Default}, nil
    })
}

// headerResolver maps the X-Tenant header to a Tenant. Replace this with your
// own OIDC-claim or token lookup as needed.
type headerResolver struct {
    fallback plugin.Tenant
}

func (r *headerResolver) Resolve(ctx context.Context) (plugin.Tenant, error) {
    id, ok := tenantIDFromContext(ctx) // your extraction: header, claim, or token
    if !ok || id == "" {
        return r.fallback, nil // no tenant on the request -> the default scope
    }
    return plugin.Tenant{
        ID:        id,
        Namespace: "tenant-" + id, // MUST be non-empty for a named tenant
        Limits:    plugin.TenantLimits{MaxApps: 25},
    }, nil
}
```

```go
// package main — your custom binary
package main

import (
    "github.com/vibed-project/vibeD/pkg/server"

    _ "github.com/yourorg/vibed-tenancy/tenant" // init() registers the resolver
)

func main() { server.Main() }
```

Build it exactly like the core:

```bash
GOTOOLCHAIN=auto GO111MODULE=on go build ./...
```

## What the deploy path does with a `Tenant`

On every deploy the service resolves the request's tenant, then:

1. **Scopes the app to the tenant namespace.** The `VibedApp` CR is created in `Tenant.Namespace` (not the server default). All quota counts and list operations are scoped to that same namespace, so tenants never see or count each other's apps.
2. **Stamps the tenant label.** When `Tenant.ID` is non-empty, the app gets the `vibed.dev/tenant` label (sanitised as a Kubernetes label value). The single-tenant default (empty `ID`) omits the label, so pre-tenancy apps are byte-for-byte unchanged.
3. **Enforces `Limits.MaxApps`.** For a **new** app (a redeploy under an existing name is never gated), the quota enforcer counts all live `VibedApp`s in the tenant namespace; if that count is already at `MaxApps`, the deploy is rejected with a `429` and `scope=tenant`, and `vibed_quota_rejections_total{scope="tenant"}` is incremented. `MaxApps: 0` disables the per-tenant cap. Per-owner and per-department ceilings from [Deploy Quotas](../configuration/quotas.md) still apply, layered on top.

## Fail-closed: a named tenant needs a namespace

A resolver that returns a **named** tenant (non-empty `ID`) but leaves `Namespace` empty is a bug — falling back to the shared default namespace would silently mix that tenant's apps in with everyone else's. The deploy path rejects it:

```
tenant "acme" resolved without a namespace
```

Only the single-tenant default (empty `ID`) is allowed to inherit the server namespace. Always set `Namespace` for every named tenant your resolver returns.

## See also

- [Deploy Quotas](../configuration/quotas.md) — per-owner and per-department ceilings that layer under the per-tenant cap.
- [Authentication & HTTPS](../configuration/authentication.md) — where the request identity (header / OIDC claim / token) your resolver reads comes from.
