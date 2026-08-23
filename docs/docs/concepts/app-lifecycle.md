---
sidebar_position: 3
---

# App Lifecycle

vibeD owns one CRD, `vibedapps.vibed.dev` (`VibedApp`, short name `vapp`, API group `vibed.dev/v1alpha1`). It's the source of truth for a deployed app; the control plane reconciles it. Everything else reuses agent-sandbox's `Sandbox` / `SandboxTemplate` / `SandboxClaim` / `SandboxWarmPool`.

## The `VibedApp` resource

```yaml
apiVersion: vibed.dev/v1alpha1
kind: VibedApp
metadata:
  name: my-app
  namespace: vibed-apps
spec:
  owner: alice@example.com          # authenticated identity (email or OIDC subject)
  source:
    tarballRef: s3://.../my-app.tar.gz   # exactly one of tarballRef / gitRef
    # gitRef: https://github.com/acme/my-app@<commit>
  runtime:
    lane: general                   # fast | general (classifier-chosen)
    template: node-24               # classifier-chosen; override allowed
    entrypoint: ""                  # optional start-command override; else autodetected
    env: []                         # [{ name, value }] passed to the user process
  egress:
    allowedHosts:                   # per-app outbound allow-list (default-deny)
      - api.openai.com
      - "*.internal.example.com"
  resources:
    cpu: "500m"                     # default 500m
    memory: "256Mi"                 # default 256Mi
  ttl: "30m"                        # idle TTL before suspend (default 30m; auto-suspend planned)
  suspended: false                  # declarative suspend/resume toggle
status:
  phase: Ready
  url: https://6fcr8uffk2pd.example.com
  sandboxRef: static-nginx-h7kzp    # the bound agent-sandbox Sandbox
  podIP: 10.244.0.42                # bound pod IP (for probe/inject, not routing)
  routeTarget: vibed-app-6fcr8uffk2pd.vibed-apps.svc.cluster.local:8080
  lastDeployedAt: 2026-05-22T10:47:23Z
  snapshotRef: ""                   # S3 URL of latest Firecracker snapshot (planned)
  conditions: [...]                 # Ready, SourceValid (metav1.Condition)
```

The app's public URL is derived from a stable 12-character label hashed from `namespace/name`, so the same app always gets the same subdomain.

## Spec fields

| Field | Type | Notes |
|---|---|---|
| `owner` | string | Authenticated user identity (email or OIDC subject). Used for AuthZ and per-user quota. Required. |
| `source.tarballRef` | string | S3 URL of the gzipped source tarball. |
| `source.gitRef` | string | Git URL + commit, used instead of a tarball. |
| `runtime.lane` | `fast` \| `general` | Runtime track. The classifier picks this; user override is rare. |
| `runtime.template` | string | Prebaked template (e.g. `node-24`, `python-313`, `static-nginx`). Empty falls back to the classifier default for the lane. |
| `runtime.entrypoint` | string | Overrides the autodetected start command in `vibed-agent`. |
| `runtime.env` | `[]{name,value}` | Environment variables exposed to the user process. |
| `egress.allowedHosts` | `[]string` | Destination hostnames the app may reach. A leading `*.` matches one or more leading labels (`*.example.com` matches `a.example.com`). Empty means the app can reach **no** external hosts. |
| `resources.cpu` | string | K8s quantity, passed through verbatim. Default `500m`. |
| `resources.memory` | string | K8s quantity, passed through verbatim. Default `256Mi`. |
| `ttl` | string | Idle TTL before suspend-to-snapshot, Go duration syntax (`30m`, `1h`). Default `30m`. |
| `suspended` | bool | Declarative suspend/resume toggle (see [Suspend & resume](#suspend--resume)). |

`source` must set **exactly one** of `tarballRef` or `gitRef`; the controller rejects a spec that sets both or neither and surfaces the reason on the `SourceValid` condition.

## Egress

The per-app egress policy is default-deny. Only the hosts in `spec.egress.allowedHosts`, plus vibeD's own system hosts (e.g. the source store), are reachable. Matching is on the CONNECT host / HTTP `Host` at the [egress authorizer](architecture.md) that fronts the per-connection Squid forward proxy. An empty list means the app has no external egress at all.

## Phases

```
Pending → Claiming → Starting → Ready ⇄ Suspended
                              ↘ Failed
```

`status.phase` is the coarse-grained lifecycle state (`+kubebuilder:validation:Enum=Pending;Claiming;Starting;Ready;Suspended;Failed`).

| Phase | Meaning |
|---|---|
| **Pending** | The CR exists; the controller hasn't started work. |
| **Claiming** | A `SandboxClaim` was created; waiting for agent-sandbox to bind an idle warm sandbox. |
| **Starting** | A sandbox is bound. The controller probes `vibed-agent` (`:9000`), then POSTs the source URL to inject + start the user process. |
| **Ready** | The user process is listening. A per-app `Service` exists, `routeTarget` + `url` are set, and vibed-router has programmed the Caddy route. |
| **Suspended** | Compute released. The `SandboxClaim` is deleted, `podIP`/`routeTarget` are cleared, and the pod is returned to the warm pool. |
| **Failed** | Validation failed, or the agent couldn't start the app. `conditions` carry the reason. |

## Status fields

| Field | Set when | Notes |
|---|---|---|
| `phase` | always | One of the phases above. |
| `url` | Ready | Public URL of the running app. Empty in Pending/Claiming/Starting. |
| `sandboxRef` | Claiming onward | Name of the bound agent-sandbox `Sandbox`. |
| `podIP` | bound onward | In-cluster IP of the bound Sandbox pod. Used to reach `vibed-agent` (`:9000`) and inject source. Cleared on Suspended. **Not** the routing target. |
| `routeTarget` | Ready | Stable upstream (`host:port`) vibed-router proxies to. See [Routing target](#routing-target). |
| `lastDeployedAt` | Ready | Timestamp of the last transition to Ready. |
| `snapshotRef` | on first suspend | S3 URL of the latest Firecracker snapshot; used to fast-path redeploys via snapshot-restore. Planned. |
| `conditions` | always | Standard `metav1.Condition` list, keyed by `type`. |

### Conditions

vibeD reports two condition types on `status.conditions`, using the standard `metav1.Condition` shape so consumers can match on `Type`/`Status`/`Reason`:

| Type | `True` means | Common reasons on `False` |
|---|---|---|
| **SourceValid** | `spec.source` resolves to exactly one of `tarballRef` / `gitRef`. | `SourceMissing`, `SourceAmbiguous` |
| **Ready** | The app is serving traffic. | `Claiming`, `Starting`, `TemplateInvalid`, `TemplateMissing`, `ClaimFailed`, `AgentUnreachable`, `InjectFailed`, `ServiceFailed`, `RouterFailed`, `Suspended` |

## Suspend & resume

`spec.suspended` is the declarative toggle the controller reconciles. Suspend is **release & re-materialize**: setting `spec.suspended=true` (or `POST /v1/apps/{app_id}/suspend`) deletes the `SandboxClaim` (returning the pod to the warm pool), parks the app in `Suspended`, and clears `podIP`/`routeTarget`. Clearing it (`POST /v1/apps/{app_id}/resume`) re-enters the normal `Claiming → Starting → Ready` machine, re-claiming a pod and re-injecting from the stored source.

In-memory and on-disk state is **not** preserved, which is appropriate for stateless web apps. Real Firecracker memory snapshots via `status.snapshotRef`, and automatic idle-TTL suspend driven by `spec.ttl`, remain future work.

## Routing target

`status.podIP` is the bound pod's IP; the controller uses it to probe and inject, but it is **not** the routing target because pod IPs change on restart. vibed-router proxies to `status.routeTarget` instead:

- **general lane**: the cluster-DNS name of a per-app `Service` that re-selects the bound pod across restarts, so it survives pod-IP churn.
- **fast lane (workerd)**: the worker's `host:port`.

When a sandbox pod is recreated, the `Service` follows it and the controller re-injects source, so the app self-heals without a route change. The router falls back to `podIP` when `routeTarget` is unset.

## HTTP endpoints

The `/v1` API drives the lifecycle above:

| Method | Path | Effect |
|---|---|---|
| `POST` | `/v1/deploy` | Classify source, create the `VibedApp`, drive it to Ready. |
| `GET` | `/v1/apps` | List the caller's apps. |
| `GET` | `/v1/apps/{app_id}` | Fetch one app (spec + status). |
| `DELETE` | `/v1/apps/{app_id}` | Delete the app and its sandbox. |
| `GET` | `/v1/apps/{app_id}/logs` | SSE live log stream. |
| `POST` | `/v1/apps/{app_id}/redeploy` | Re-inject the latest source into a fresh sandbox. |
| `POST` | `/v1/apps/{app_id}/suspend` | Set `spec.suspended=true`. |
| `POST` | `/v1/apps/{app_id}/resume` | Set `spec.suspended=false`. |

## Implemented vs. planned

Implemented: the deploy → `Ready` path, `GET`/`DELETE` of apps, `POST /v1/apps/{app_id}/redeploy`, `GET /v1/apps/{app_id}/logs` (SSE live log streaming), and `POST /v1/apps/{app_id}/suspend` + `.../resume`. Still design targets: real snapshot-restore (`status.snapshotRef`) and automatic idle-TTL suspend driven by `spec.ttl`.
