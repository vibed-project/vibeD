---
sidebar_position: 4
---

# Control Plane

vibeD's control plane is a set of small, stateless Go binaries that turn a `VibedApp` CR into a running, routable app. It owns exactly one CRD — [`VibedApp`](app-lifecycle.md) — and reuses [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)'s `Sandbox`, `SandboxTemplate`, `SandboxClaim`, and `SandboxWarmPool` for the data plane.

Durable state lives in the Kubernetes API (the `VibedApp` CRs are the source of truth) plus the source-blob store — the binaries below hold no state of their own and are safe to restart or scale.

## The six binaries

| Binary | Package | Responsibility |
|---|---|---|
| **vibed** | `cmd/vibed` | Control plane. Stateless HTTP server: authN/Z, multipart source upload, the classifier, `VibedApp` CR creation, plus the React dashboard and the MCP server. |
| **vibed-controller** | `cmd/vibed-controller` | The `VibedApp` reconciler / state machine. Claims a warm sandbox, probes the agent, injects source, creates the per-app Service, publishes the URL. |
| **vibed-router** | `cmd/vibed-router` | Watches `VibedApp` status and programs **Caddy** routes via its admin API. Read-only on the K8s side; idempotent and safe to run N replicas. |
| **vibed-agent** | `cmd/vibed-agent` | In-sandbox PID-1 agent baked into every template image. Exposes vibeD's control API; pulls the source tarball into `/workspace`, starts and supervises the user process. |
| **vibed-egress-authz** | `cmd/vibed-egress-authz` | Per-connection egress authorizer for the Squid forward proxy. Maps a source pod IP to the owning `VibedApp`'s allow-list and returns allow/deny. Default-deny. |
| **vibed-workerd-loader** | `cmd/vibed-workerd-loader` | Fast-lane workerd sidecar. Receives a deploy from the controller, fetches the tarball, writes the worker module, re-renders `config.capnp`, and reloads workerd. |

### Metrics & health ports

Ports are the built-in defaults; each flag/env var below overrides it.

| Binary | Metrics | Health / readiness | Service API |
|---|---|---|---|
| **vibed** | `/metrics` on the HTTP server (`:8080`, `server.httpAddr`) | `/healthz`, `/readyz` on the same address | HTTP `/v1` + MCP + dashboard on `:8080` |
| **vibed-controller** | `:8081` (`--metrics-bind-address`) | `:8082` (`--health-probe-bind-address`); `healthz`, `readyz` | — (watches the API server) |
| **vibed-router** | `:8083` (`--metrics-bind-address`) | `:8084` (`--health-probe-bind-address`); `healthz`, `readyz` | Caddy admin at `--caddy-admin` (default `http://localhost:2019`) |
| **vibed-agent** | — | `GET /healthz` on the control API | Control API on `:9000` (`VIBED_AGENT_CONTROL_ADDR`); user app on `:8080` (`VIBED_AGENT_APP_PORT`) |
| **vibed-egress-authz** | `--metrics-bind-address` (default `0` = disabled) | — | `GET /authz` on `:8090` (`--addr`) |
| **vibed-workerd-loader** | — | — | Control API on `:9200` (`--listen`) |

:::note
`vibed` serves `/healthz`, `/readyz`, and `/metrics` on its main HTTP listener rather than a separate port — the auth middleware skips these paths. The controller and router use controller-runtime's split metrics/probe servers, so they get dedicated ports. The agent, egress authorizer, and workerd loader are single-purpose HTTP services with no separate metrics server by default.
:::

## The reconcile loop

`vibed-controller` is a controller-runtime reconciler. It watches `VibedApp` and — via a cross-resource watch — `SandboxClaim`, so a status change on the claim (agent-sandbox binding a pod, the pod becoming ready) wakes the owning `VibedApp` immediately instead of relying on polling. The `SandboxClaim` and its `VibedApp` share namespace/name by convention, which is how the watch maps one to the other.

Each `Reconcile` performs a **single hop** of the state machine and then either requeues (for transitional phases) or returns and waits for the next event. This keeps every step observable in `status` before the next one runs. Run the controller as a Deployment with `--leader-elect` for HA — replicas stand by without competing reconciles.

Two side branches run before the main phase switch:

- **Spec validation** — `spec.source` must set exactly one of `tarballRef` or `gitRef`, and `spec.runtime.lane` is required. A validation failure sets `SourceValid=False` and moves the app to `Failed` until the user fixes the spec.
- **Suspend/restore** — `spec.suspended` is the declarative toggle. Setting it releases the backing compute (the `SandboxClaim`, or the workerd worker) and parks the app in `Suspended`; clearing it re-enters the claim machine.

The **workerd fast lane** collapses the whole lifecycle into one step: there is no warm-pool claim or agent, so the loader deploys the script directly and a successful deploy is immediately `Ready`. The state machine below describes the **general lane** (and the static-nginx fast lane, which still runs on a sandbox pod).

## State machine

```text
Pending ──▶ Claiming ──▶ Starting ──▶ Ready
              │             │           │
              ▼             ▼           │ (sandbox pod replaced)
            Failed        Failed        └──▶ Starting
              ▲             ▲
              └─────────────┴──── (any) ──▶ Failed

Ready/Claiming/Starting ──(spec.suspended)──▶ Suspended ──(clear)──▶ Claiming
```

| Phase | Meaning |
|---|---|
| `Pending` | Freshly created (or unknown phase). The reconciler immediately advances it to `Claiming`. |
| `Claiming` | Waiting on a warm sandbox. A `SandboxClaim` exists; agent-sandbox has not yet bound a pod. |
| `Starting` | A pod is bound. The controller probes the agent, injects source, creates the per-app Service, and publishes the route. |
| `Ready` | The user process is listening and the route is live. `status.url` is set. |
| `Suspended` | Backing compute released via `spec.suspended`. `podIP`, `sandboxRef`, and `routeTarget` are cleared. |
| `Failed` | Terminal for this spec generation — invalid spec, no warm pool for the template, or a template image that failed validation. |

### Key transitions

1. **`Pending` → `Claiming`.** Set `Ready=False` with reason `Claiming` and requeue.

2. **`Claiming` → `Starting`.** The controller hard-gates on base-image validation (`Gate.Allowed`), then calls `Claimer.EnsureClaim`, which creates or reads the app's `SandboxClaim` against the warm pool. `EnsureClaim` is idempotent and returns `bound`:
   - `bound == false` is a normal outcome — the claim exists but no pod is bound yet. Stay in `Claiming`; the `SandboxClaim` watch wakes the loop when a pod binds, with a periodic requeue as a backstop.
   - `bound == true` records `status.sandboxRef` and `status.podIP` and advances to `Starting`.

3. **`Starting` → `Ready`.** Reached the bound pod's IP:
   - **Probe the agent** at `podIP:9000` via `AgentProbe.IsReady`. Unreachable → `AgentUnreachable`; reachable-but-not-listening → requeue.
   - **Inject source** via `Injector.Inject` — the controller POSTs the source URL to `vibed-agent`, which pulls the tarball into `/workspace` and starts the user process. Idempotent: re-injecting on a requeue replaces the running process.
   - **Create the per-app Service** via `Services.EnsureService`, which returns a stable upstream (`RouteTarget`). The Service re-selects the bound pod across restarts, so the route survives pod-IP churn.
   - **Publish the route** via `Router.Publish`, which computes the app's URL from a stable hash of `namespace/name` (no side effects). Set `status.url`, `status.lastDeployedAt`, `Ready=True` (`Running`), and move to `Ready`. `vibed-router` observes the `Ready` status out-of-band and programs the actual Caddy route.

4. **`Ready` → `Starting` (self-heal).** On every `Ready` reconcile the controller re-checks the claim. If agent-sandbox rebound a **replacement pod** (new `podIP`), the fresh workspace lost the injected source, so the app drops back to `Starting` to re-probe and re-inject. The per-app Service already follows the new pod, so the route stays valid and the app self-heals.

5. **Suspend / restore.** `spec.suspended=true` releases compute (`ReleaseClaim`, or `FastLane.Remove` for workerd), clears the pod/route status, and parks in `Suspended`. Clearing it flips back to `Claiming` to re-claim and re-inject — in-memory state is not preserved.

## Conditions

The reconciler writes two condition types into `status.conditions`:

- **`Ready`** — the headline condition. `True` once the app serves traffic, `False` otherwise, with a reason describing where in the machine it is.
- **`SourceValid`** — `True` when `spec.source` resolves to exactly one of `tarballRef` / `gitRef`; the user-visible reason when spec validation fails.

Main condition reasons:

| Reason | Condition | When |
|---|---|---|
| `Claiming` | `Ready=False` | Waiting on the warm pool to bind a pod. |
| `Starting` | `Ready=False` | Agent up; injecting source / user process starting. |
| `Running` | `Ready=True` | User process listening; route published. |
| `SourceMissing` | `SourceValid=False` | Neither `tarballRef` nor `gitRef` set. |
| `SourceAmbiguous` | `SourceValid=False` | Both `tarballRef` and `gitRef` set. |
| `ClaimFailed` | `Ready=False` | Transient error creating/reading the `SandboxClaim` (retried). |
| `TemplateMissing` | `Ready=False` (→ `Failed`) | No warm pool backs the template — terminal. |
| `TemplateInvalid` | `Ready=False` (→ `Failed`) | The template's base image failed validation — terminal. |
| `AgentUnreachable` | `Ready=False` | The agent control API at `podIP:9000` could not be reached. |
| `InjectFailed` | `Ready=False` | The agent rejected or failed the source injection. |
| `ServiceFailed` | `Ready=False` | Creating the per-app Service failed. |
| `RouterFailed` | `Ready=False` | Publishing the route (URL) failed. |
| `Suspended` | `Ready=False` | App suspended; compute released. |

## See also

- [Architecture](architecture.md) — control plane vs. data plane and the deploy flow.
- [App Lifecycle](app-lifecycle.md) — the `VibedApp` resource and its `spec`/`status` fields.
- [Lanes & Templates](lanes-and-templates.md) — how the classifier picks a lane and template.
- [Configuration Reference](../configuration/config-reference.md) — the controller and router flags wired from Helm values.
