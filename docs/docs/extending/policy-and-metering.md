---
sidebar_position: 1
---

# Policy and Metering

vibeD exposes two seams on the deploy path so operators and integrators can plug in
governance and usage accounting **without forking the core**:

- **`PolicyGate`** — a deploy-time decision hook. It runs after the classifier picks
  a lane and template and **before** the `VibedApp` is created, and may reject the deploy.
- **`MeterSink`** — a usage-event hook. It records `deploy`/`delete` events from the
  server and `app.ready`/`app.stopped` lifecycle transitions from the controller.

Both are ordinary Go extensibility: you build your **own** out-of-tree module, implement the
interface, register it from an `init()`, and start the server through `server.Main()` (see
[Building the binary](#building-the-binary)). Nothing here is coupled to a specific
implementation — the core ships a neutral default for each seam (no gate; a Prometheus
meter).

The exported types live in package
[`github.com/vibed-project/vibeD/pkg/plugin`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/plugin).
`pkg/plugin` re-exports the internal registry interfaces as **type aliases**, so a type in
your module that satisfies `plugin.PolicyGate` also satisfies the internal interface — no
adapter needed.

:::info Both seams are single-slot
There is at most **one** registered gate and **one** registered meter sink per process.
`RegisterPolicyGate` / `RegisterMeterSink` panic on a second registration. Compose multiple
meter sinks with [`TeeMeterSinks`](#composing-sinks) instead of registering twice.
:::

## PolicyGate

A `PolicyGate` evaluates every deploy — **new apps and redeploys alike**, since a redeploy
can introduce a new violation — after classification and before anything is stored or created.
Returning a denial aborts the deploy and the API responds `403 Forbidden`; nothing is
persisted.

There is **no gate by default**: a plain build registers none, so deploys are unrestricted
and behavior is unchanged until you register one.

### The interface

```go
type PolicyGate interface {
    Evaluate(ctx context.Context, in PolicyInput) error
}
```

A non-nil return denies the deploy. To get the `403` mapping, return a
`*PolicyDeniedError` (or any error whose `PolicyDenied() bool` method returns `true`); any
other error is treated as a denial as well but is not specially typed.

```go
// PolicyDeniedError marks a policy denial. Its Error() reads
// "deploy denied by policy: <Reason>".
type PolicyDeniedError struct {
    Reason string
}
```

### PolicyInput

`PolicyInput` carries everything the classifier resolved, plus the raw source bytes:

| Field | Type | Notes |
|-------|------|-------|
| `Tenant` | `plugin.Tenant` | Resolved tenant (`ID`, `Namespace`, `Limits`); `ID` is empty for the single-tenant default. |
| `Owner` | `string` | Deploying identity. |
| `Name` | `string` | App name (a DNS label). |
| `Lane` | `vibedv1.Lane` | Chosen lane (`fast` / `general`), after any override. |
| `Template` | `string` | Chosen warm-pool template, after any override. |
| `AllowedHosts` | `[]string` | Per-app egress allow-list; empty means no external egress. |
| `Env` | `[]vibedv1.EnvVar` | Environment variables for the app. |
| `IsNew` | `bool` | `true` for a first deploy, `false` for a redeploy under an existing name. |
| `Source` | `[]byte` | The uploaded source tarball (gzip'd), for content policies such as secret scanning. **Read-only; do not retain it past `Evaluate`.** |

This is enough to enforce, for example: allowed templates or lanes, egress destinations,
guardrails on env values, secrets-in-source scanning, or per-tenant ceilings.

### Registering a gate

```go
func RegisterPolicyGate(f func(PolicyDeps) (PolicyGate, error))
```

`RegisterPolicyGate` installs the factory (called once at startup with a
`PolicyDeps{Options map[string]string}` settings bag). Call it from your package's `init()`.

### Skeleton

```go
package mypolicy

import (
    "context"
    "strings"

    "github.com/vibed-project/vibeD/pkg/plugin"
)

// gate denies deploys whose source appears to embed a private key.
type gate struct{}

func (gate) Evaluate(_ context.Context, in plugin.PolicyInput) error {
    if strings.Contains(string(in.Source), "BEGIN PRIVATE KEY") {
        return &plugin.PolicyDeniedError{Reason: "source contains an embedded private key"}
    }
    return nil
}

func init() {
    plugin.RegisterPolicyGate(func(_ plugin.PolicyDeps) (plugin.PolicyGate, error) {
        return gate{}, nil
    })
}
```

Wire it into a custom binary by blank-importing the package so its `init()` runs, then
starting the server:

```go
package main

import (
    _ "example.com/mypolicy"

    "github.com/vibed-project/vibeD/pkg/server"
)

func main() { server.Main() }
```

:::info `Source` is gzip'd
`PolicyInput.Source` is the raw gzip'd tarball. For byte-pattern checks you can scan it as
shown; for path- or file-content policies, decompress and walk the tar entries yourself.
Treat the slice as borrowed — copy anything you need to keep.
:::

## MeterSink

A `MeterSink` records one `MeterEvent` per usage event. It is **best-effort and off the
critical path**: `Record` returns no error and must not block for long. Metering never
fails a deploy.

### The interface

```go
type MeterSink interface {
    Record(ctx context.Context, e MeterEvent)
}
```

### MeterEvent

```go
type MeterEvent struct {
    Kind      string // event type; see below
    Tenant    string // tenant ID ("default" for the single-tenant default)
    Owner     string
    App       string
    Namespace string
}
```

`Kind` is one of:

| Kind | Emitted by | When |
|------|------------|------|
| `deploy` | server (deploy service) | A deploy succeeds and the `VibedApp` is created/updated. |
| `delete` | server (deploy service) | An app is deleted. |
| `app.ready` | controller | The app reaches `Ready`. |
| `app.stopped` | controller | The app stops. |

An `app.ready` → `app.stopped` pair bounds a runtime interval, which is what you'd
integrate over to account for compute usage.

### The default sink

When no sink is registered, the core installs a **Prometheus** sink that counts events by
kind and tenant:

```
vibed_usage_events_total{kind,tenant}
```

The `tenant` label is the tenant **ID** (not name) to bound cardinality; the single-tenant
default reports `tenant="default"`. This series is exposed on the standard
[`/metrics` endpoint](../deployment/monitoring.md) alongside the other vibeD metrics.

### Registering a sink

```go
func RegisterMeterSink(f func(MeterDeps) (MeterSink, error))
```

Registering a sink **replaces** the Prometheus default. To keep the built-in metric while
adding your own accounting, compose them (see below). Two helpers are provided:

```go
func PrometheusMeterSink() MeterSink             // the core's default sink
func TeeMeterSinks(sinks ...MeterSink) MeterSink // fan one event out to many sinks
```

### Skeleton

```go
package mymeter

import (
    "context"
    "log/slog"

    "github.com/vibed-project/vibeD/pkg/plugin"
)

// sink logs every usage event as structured JSON.
type sink struct{}

func (sink) Record(_ context.Context, e plugin.MeterEvent) {
    slog.Info("usage",
        "kind", e.Kind,
        "tenant", e.Tenant,
        "owner", e.Owner,
        "app", e.App,
        "namespace", e.Namespace,
    )
}

func init() {
    plugin.RegisterMeterSink(func(_ plugin.MeterDeps) (plugin.MeterSink, error) {
        return sink{}, nil
    })
}
```

### Composing sinks

Because registering replaces the default, tee your sink with `PrometheusMeterSink()` to keep
the `vibed_usage_events_total` metric:

```go
plugin.RegisterMeterSink(func(_ plugin.MeterDeps) (plugin.MeterSink, error) {
    return plugin.TeeMeterSinks(
        plugin.PrometheusMeterSink(), // keep the built-in metric
        sink{},                       // plus your own accounting
    ), nil
})
```

`TeeMeterSinks` fans each event out to every sink in order and skips nil entries.

## Building the binary

A build that uses either seam is an ordinary Go module that depends on
`github.com/vibed-project/vibeD`, blank-imports its provider package(s), and calls
`server.Main()`. Build it the same way as the core:

```bash
GOTOOLCHAIN=auto GO111MODULE=on go build ./...
```

Package `github.com/vibed-project/vibeD/pkg/server` exposes the full entrypoint —
`server.Main()`, `server.Run(cfg, logger)`, `server.LoadConfig(path)`, and
`server.NewLogger(cfg)` — and `pkg/plugin` re-exports the other registerable seams (stores,
auth providers, tenancy, secret schemes, feature-gating) the same way as the two documented
here.
