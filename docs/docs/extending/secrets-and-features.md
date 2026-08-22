---
sidebar_position: 3
---

# Secret Schemes & Feature Gating

Two small extension seams live in [`pkg/plugin`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/plugin). Both let an **out-of-tree Go module** change vibeD's behavior without patching the core: you register your implementation from an `init()`, then build a custom binary that blank-imports your package and calls `server.Main()`.

```go
package main

import (
	_ "example.com/vibed-providers/mystuff" // its init() calls the Register funcs
	"github.com/vibed-project/vibeD/pkg/server"
)

func main() { server.Main() }
```

See the [extension overview](./overview.md) for the general pattern (stores, auth, tenancy, policy, metering). This page covers the two remaining seams: **secret schemes** and **feature gating**.

## Secret schemes

Config values that hold secrets can be **scheme-prefixed**: `<scheme>:<ref>`. vibeD resolves the reference at load time so a plaintext secret never has to sit in `vibed.yaml`. Two schemes are built in:

| Value | Resolves to |
| --- | --- |
| `env:DB_PASSWORD` | the value of environment variable `DB_PASSWORD` |
| `file:/var/run/secrets/db` | the contents of that file, trimmed of surrounding whitespace |

A value that has **no scheme** (or a scheme that nobody registered) is returned **unchanged**, so a literal password like `hunter2` (or a value that just happens to contain a `:`) passes through as-is.

```yaml
auth:
  oidc:
    clientSecret: "env:OIDC_CLIENT_SECRET"   # read from the environment
storage:
  tarball:
    s3:
      # a literal value with no registered scheme is used verbatim
      bucket: "vibed-sources"
```

:::info Unknown schemes pass through
Resolution splits on the first `:`. If the token before it is not a registered scheme, the **whole original value** is returned literally; an unknown scheme is never an error. That keeps arbitrary secret strings safe, but it also means a typo (`evn:FOO` instead of `env:FOO`) fails silently by passing `evn:FOO` through. Double-check scheme spellings.
:::

### Registering a custom scheme

`RegisterSecretScheme(scheme, resolver)` adds a scheme, for example one that reads from an external secret store. The resolver receives the **reference part only** (everything after the first `:`) plus a `context.Context`, and returns the resolved value or an error.

```go
// SecretSchemeResolver = func(ctx context.Context, ref string) (string, error)
func RegisterSecretScheme(scheme string, r SecretSchemeResolver)
```

For a value like `vault:secret/data/db#password`, your resolver is called with `ref == "secret/data/db#password"`.

```go
package myvault

import (
	"context"

	"github.com/vibed-project/vibeD/pkg/plugin"
)

func init() {
	plugin.RegisterSecretScheme("vault", resolve)
}

// resolve receives everything after the first ':' in the config value.
func resolve(ctx context.Context, ref string) (string, error) {
	// ref == "secret/data/db#password"
	path, field := splitPathAndField(ref)
	secret, err := vaultClient.Read(ctx, path)
	if err != nil {
		return "", err
	}
	return secret[field], nil
}
```

Notes:

- **Register from an `init()`.** Registration is process-wide; registering the same scheme twice **panics** at startup (a duplicate is a programmer error). `env` and `file` are already registered too, so do not re-register them.
- **The `context` is honored.** vibeD passes a context through to your resolver so external fetches can be bounded or cancelled; respect it for network calls.
- An empty scheme name or a `nil` resolver also panics; these surface immediately at startup rather than at deploy time.

## Feature gating

The `Entitlements` seam is a process-wide **feature-flag** mechanism. It lets a build advertise a named **edition** and gate optional code paths behind named features. It is plain extensibility: an integrator who ships their own out-of-tree providers can use it to turn provider registrations on or off from a single switch.

:::note The default enables everything
The core **never gates itself**, and the default edition enables **all** features. Out of the box nothing is gated: every `RequireFeature` call that the core makes (and it makes none for its own behavior) would succeed. Feature gating only takes effect once *your* build installs a non-default edition and *your* providers choose to consult it.
:::

### The two calls

```go
// Entitlements reports the running edition and which features it enables.
type Entitlements interface {
	Edition() string             // names the running edition
	Enabled(feature string) bool // reports whether a named feature is on
}

// SetEntitlements installs a process-wide edition descriptor (call once, at startup).
func SetEntitlements(e Entitlements)

// RequireFeature returns nil if the feature is enabled, else a descriptive error.
func RequireFeature(feature string) error
```

- `SetEntitlements(e)` installs a process-wide edition descriptor. Call it **once**, at startup, before providers register. Passing `nil` resets to the default. The core never calls it.
- `RequireFeature(name)` is a **gate a provider may call to enable or disable itself**. It returns `nil` when the current edition enables `name`, or an error describing which edition is running and which feature was refused.

### Skeleton

Define an edition, install it at startup, and have a provider gate its own registration behind a feature:

```go
package myedition

import (
	"log"

	"github.com/vibed-project/vibeD/pkg/plugin"
)

// edition implements plugin.Entitlements.
type edition struct {
	name     string
	features map[string]bool
}

func (e edition) Edition() string            { return e.name }
func (e edition) Enabled(feature string) bool { return e.features[feature] }

func init() {
	// Advertise an edition that turns specific features on.
	plugin.SetEntitlements(edition{
		name:     "internal",
		features: map[string]bool{"multi-tenant": true},
	})

	// A provider consults the gate before activating itself.
	if err := plugin.RequireFeature("multi-tenant"); err != nil {
		log.Printf("multi-tenant provider disabled: %v", err)
		return
	}
	plugin.RegisterTenantResolver(newTenantResolver)
}
```

Because the default edition enables every feature, a build that does **not** call `SetEntitlements` sees every `RequireFeature` succeed, so the gate is opt-in and inert until you install an edition that returns `false` from `Enabled` for the features you want to withhold.

## See also

- [Extension overview](./overview.md): the store / auth / tenancy / policy / metering seams and the custom-binary pattern.
- [Configuration reference](../configuration/config-reference.md): where secret-bearing values live in `vibed.yaml`.
