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
- **Sandboxes** — a claimed warm sandbox runs the user's app. General-lane sandboxes are Kata + Firecracker microVMs; the fast lane uses workerd isolates / static nginx.
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

## CRD ownership

vibeD owns exactly one CRD, `vibedapps.vibed.dev`. See [App lifecycle](app-lifecycle.md) for the schema and phases. Everything else is an agent-sandbox CRD.

:::warning
The `VibedApp` CRD ships in the Helm chart's `crds/` directory, which Helm installs **only on first install and never upgrades**. When the CRD schema changes between versions you must `kubectl apply` the updated CRD manually — see [troubleshooting](../deployment/troubleshooting.md).
:::
