---
sidebar_position: 1
---

# Architecture

vibeD splits into a **control plane** (stateless, runs in your system namespace) and a **data plane** (the warm pools and the sandboxes that serve user traffic). It is built on top of [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) — vibeD adds one CRD (`VibedApp`) and reuses agent-sandbox's `Sandbox`, `SandboxTemplate`, `SandboxClaim`, and `SandboxWarmPool`.

## Control plane

| Component | Binary | Role |
|---|---|---|
| **vibed** | `cmd/vibed` | Stateless HTTP server. AuthN/Z, accepts the multipart upload, stores the source blob, runs the classifier, creates the `VibedApp` CR, and serves both the dashboard and the MCP server. |
| **vibed-controller** | `cmd/vibed-controller` | controller-runtime reconciler. Drives each `VibedApp` through its lifecycle: claims a warm sandbox, injects source, probes readiness, publishes the URL. |
| **vibed-router** | `cmd/vibed-router` | Watches `VibedApp` status and programs **Caddy** routes via its admin API. Read-only on the K8s side, so it scales to N replicas. |
| **Caddy** | upstream image | The reverse proxy for `*.<domain>`. Wildcard DNS-01 TLS in production; plain HTTP in dev. |

The control plane is stateless. Durable state lives in the Kubernetes API (the `VibedApp` CRs are the source of truth) plus the source-blob store (see [storage](../configuration/storage.md)).

## Data plane

- **Warm pools** — one `SandboxWarmPool` + `SandboxTemplate` per runtime template, pre-booting idle sandboxes so a deploy never waits on a cold boot or an image build.
- **Sandboxes** — a claimed warm sandbox runs the user's app. General-lane sandboxes are Kata microVMs (QEMU by default, or Firecracker on KVM/bare-metal); the fast lane uses workerd isolates / static nginx.
- **vibed-agent** — a small Go binary baked into every template image, running as PID 1 inside the sandbox. The controller POSTs it the source URL; it pulls, extracts into `/workspace`, autodetects the entrypoint, and starts the user process. It exposes a control API on `:9000` and the app listens on `:8080`.

## The deploy flow

1. **Upload & classify** — `POST /v1/deploy` streams the gzipped source tarball to the blob store. The [classifier](lanes-and-templates.md) reads only file *names* + `package.json` keys and picks a `(lane, template)`.
2. **Create `VibedApp`** — the server writes a `VibedApp` CR in the workloads namespace and watches its `status.phase` (up to a 10s budget).
3. **Claim** — the controller creates a `SandboxClaim` against the template's warm pool; agent-sandbox binds an idle sandbox.
4. **Inject** — the controller POSTs the source URL to that sandbox's `vibed-agent` (`:9000`), which pulls and starts the app.
5. **Publish** — once the app is listening, the controller creates a per-app `Service`, sets `status.routeTarget` + `status.url`, and flips the app to `Ready`.
6. **Route** — vibed-router observes `Ready` and programs a Caddy route matching `<id>.<domain>` → the per-app Service.

If everything is warm this completes in seconds and `/v1/deploy` returns `200` with the URL. If the classifier picks a path that isn't warm yet (e.g. a runtime needing an async template build), the API returns `202` + a `status_url` to poll — an explicit degraded mode, not the default.

### Per-app Service routing

Caddy proxies to a **per-app `Service`** (`vibed-app-<label>`), not the sandbox pod IP. The Service selects the bound pod by its `agents.x-k8s.io/claim-uid` label, so if the sandbox pod is recreated with a new IP, the Service follows it and the route never goes stale. On a pod restart the controller also re-injects source into the fresh workspace, so the app self-heals.

## Namespaces

The control plane runs in the release namespace (e.g. `vibed-system`). All workloads — `VibedApp` CRs, `SandboxClaim`s, `SandboxTemplate`s, and the warm pools — share **one** namespace (`vibed-apps` by default).

:::note
The original design (`refactor.md` §7.1) split the data plane into `vibed-pools` + `vibed-apps`. In practice agent-sandbox binds a `SandboxClaim` only within the namespace its `SandboxTemplate` lives in — there is no cross-namespace pod move — so VibedApps, claims, templates, and warm pools must co-locate. vibeD uses a single workloads namespace.
:::

## Tenant model

Every request runs inside a **tenant** — an isolation scope of `(ID, Namespace, Limits)`. By default the control plane runs a **single implicit tenant**: an empty ID, the server-default apps namespace, and no per-tenant limits. Behavior is identical to a build with no notion of tenancy: all apps land in the one workloads namespace described above.

The tenant is resolved per request by a `TenantResolver`. The built-in resolver (`SingleTenant`) yields the same default tenant for every request. An out-of-tree module can register its own resolver to map each request to its own namespace and limits (`TenantLimits.MaxApps` caps live apps per tenant); the core then scopes each deploy to the resolved namespace instead of the single default. See [Tenancy](../extending/tenancy.md).

## Extension points

The control plane keeps its pluggable seams behind a stable public package, [`pkg/plugin`](https://pkg.go.dev/github.com/vibed-project/vibeD/pkg/plugin). A **separate** Go module supplies its own implementation, blank-imports its provider package (whose `init()` calls the relevant `Register*` function), and then calls `server.Main()` to run a custom `vibed` binary. Because the package re-exports the core interfaces as **type aliases**, an implementation that satisfies `plugin.ArtifactStore` also satisfies the internal interface — no adapter is needed.

| Seam | Registry | Selected by | Default |
|---|---|---|---|
| **Store backend** | `RegisterStoreBackend(name, factory)` | `store.backend` config | built-in `sqlite` / `configmap` |
| **Auth provider** | `RegisterAuthProvider(mode, factory)` | `auth.mode` config | built-in `apikey` / `oidc` |
| **Tenancy** | `RegisterTenantResolver(factory)` | at most one, process-wide | single implicit tenant |
| **Policy gate** | `RegisterPolicyGate(factory)` | at most one, process-wide | no gate (deploys unrestricted) |
| **Metering sink** | `RegisterMeterSink(factory)` | at most one, process-wide | `PrometheusMeterSink` |
| **Secret scheme** | `RegisterSecretScheme(scheme, resolver)` | `<scheme>:` prefix in a secret value | built-in `env:` / `file:` |

A store backend implements `ArtifactStore` and may additionally implement `UserStore`, `AuditStore`, `ShareLinkStore`, and `io.Closer`; the core feature-detects those by type assertion. An auth provider contributes a `TokenVerifier` plus any public `Route`s its login flow needs. `TeeMeterSinks` composes a custom sink with the Prometheus default.

Feature availability is governed by an `Entitlements` seam (`SetEntitlements` / `RequireFeature`). The core never sets entitlements, and the default edition enables **all** features; an out-of-tree build can install its own edition to gate features on or off.

For the full guide and per-seam examples, see the [Extending](../extending/overview.md) section.

## CRD ownership

vibeD owns exactly one CRD, `vibedapps.vibed.dev`. See [App lifecycle](app-lifecycle.md) for the schema and phases. Everything else is an agent-sandbox CRD.

:::warning
The `VibedApp` CRD ships in the Helm chart's `crds/` directory, which Helm installs **only on first install and never upgrades**. When the CRD schema changes between versions you must `kubectl apply` the updated CRD manually — see [troubleshooting](../deployment/troubleshooting.md).
:::
