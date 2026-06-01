---
sidebar_position: 3
---

# App Lifecycle

vibeD owns one CRD, `vibedapps.vibed.dev` (`VibedApp`, short name `vapp`). It's the source of truth for a deployed app; the control plane reconciles it.

## The `VibedApp` resource

```yaml
apiVersion: vibed.dev/v1alpha1
kind: VibedApp
metadata:
  name: my-app
  namespace: vibed-apps
spec:
  owner: alice@example.com        # authenticated identity
  source:
    tarballRef: s3://.../my-app.tar.gz   # exactly one of tarballRef / gitRef
  runtime:
    lane: general                 # fast | general (classifier-chosen)
    template: node-24             # classifier-chosen; override allowed
    entrypoint: ""                # optional override; else autodetected
    env: []
  resources: { cpu: "500m", memory: "256Mi" }
  ttl: "30m"                      # idle TTL before suspend (planned)
status:
  phase: Ready
  url: https://6fcr8uffk2pd.example.com
  sandboxRef: static-nginx-h7kzp  # the bound agent-sandbox Sandbox
  podIP: 10.244.0.42              # bound pod IP (for probe/inject, not routing)
  routeTarget: vibed-app-6fcr8uffk2pd.vibed-apps.svc.cluster.local:8080
  lastDeployedAt: 2026-05-22T10:47:23Z
  conditions: [...]
```

The app's public URL is derived from a stable 12-character label hashed from `namespace/name`, so the same app always gets the same subdomain.

## Phases

```
Pending → Claiming → Starting → Ready ⇄ Suspended
                              ↘ Failed
```

| Phase | Meaning |
|---|---|
| **Pending** | The CR exists; the controller hasn't started work. |
| **Claiming** | A `SandboxClaim` was created; waiting for agent-sandbox to bind an idle warm sandbox. |
| **Starting** | A sandbox is bound. The controller probes `vibed-agent` (`:9000`), then POSTs the source URL to inject + start the user process. |
| **Ready** | The user process is listening. A per-app `Service` exists, `routeTarget` + `url` are set, and vibed-router has programmed the Caddy route. |
| **Failed** | Validation failed, or the agent couldn't start the app. `conditions` carry the reason. |
| **Suspended** | Compute released. Setting `spec.suspended=true` (or `POST /v1/apps/{id}/suspend`) deletes the `SandboxClaim` — returning the pod to the warm pool — and clears `podIP`/`routeTarget`. Clearing it (`POST .../resume`) flips back to `Claiming`, re-claiming a pod and re-injecting source. |

## Suspend & resume

`spec.suspended` is the declarative toggle the controller reconciles. Suspend is **release & re-materialize**: the warm-pool pod is freed and the app parked in `Suspended`; resume re-enters the normal `Claiming → Starting → Ready` machine, re-injecting from the stored source. In-memory/disk state is **not** preserved — appropriate for stateless web apps. (Real Firecracker memory snapshots via `status.snapshotRef`, and automatic idle-TTL suspend driven by `spec.ttl`, remain future work.)

## Routing target

`status.podIP` is the bound pod's IP — the controller uses it to probe and inject, but it is **not** the routing target because pod IPs change on restart. The router proxies to `status.routeTarget`: the cluster-DNS name of a per-app `Service` that selects the bound pod by its claim UID. When a sandbox pod is recreated, the Service follows it and the controller re-injects source, so the app self-heals without a route change.

## Implemented vs. planned

Implemented: the deploy → `Ready` path, `GET`/`DELETE` of apps, `POST /v1/apps/{id}/redeploy`, `GET /v1/apps/{id}/logs` (SSE live log streaming), and `POST /v1/apps/{id}/suspend` + `.../resume`. Still design targets: real snapshot-restore (`status.snapshotRef`) and automatic idle-TTL suspend.
