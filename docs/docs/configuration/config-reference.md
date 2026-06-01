---
sidebar_position: 1
---

# Configuration Reference

There are two layers of configuration:

1. **Application config** (`vibed.yaml`) — runtime settings for the `vibed` server. In Kubernetes this is rendered into a ConfigMap by the Helm chart and mounted at `/etc/vibed/vibed.yaml`.
2. **Helm values** — cluster topology: namespaces, RuntimeClass, NetworkPolicy, the controller/router/caddy components, and the warm pools.

You normally set everything through Helm values; the chart renders the app config for you.

## Application config (`vibed.yaml`)

```yaml
server:
  transport: "http"            # stdio | http
  httpAddr: ":8080"
  baseURL: ""                  # public URL for share-link generation
  rateLimit: { enabled: false, requestsPerSecond: 10, burst: 20 }

auth:
  enabled: false               # enable before exposing the API
  mode: "apikey"               # apikey | oidc
  # oidc: { issuer, audience, usernameClaim, roleClaim, adminRole, ... }

deployment:
  namespace: "vibed-apps"      # default deploy namespace
  appsNamespace: "vibed-apps"  # where /v1 creates VibedApps + warm pools live
  readyTimeout: "10m"          # how long a deploy waits for Ready before failing

storage:
  backend: "local"
  tarball:                     # source-blob store for /v1/deploy
    backend: "served"          # served (DEV only) | s3 (PRODUCTION)
    served:
      basePath: "/data/vibed/sources"
      publicBaseURL: ""        # empty -> in-cluster Service DNS
    s3:
      endpoint: ""             # empty for AWS; set for MinIO
      bucket: ""
      region: ""
      presignTTL: "15m"

store:
  backend: "sqlite"            # sqlite | configmap
  sqlite:    { path: "/data/vibed/vibed.db" }
  configmap: { name: "vibed-artifacts" }

limits: { maxTotalFileSize: 52428800, maxFileCount: 500, maxLogLines: 10000 }
tracing: { enabled: false, endpoint: "", sampleRate: 1.0 }
gc:      { enabled: true, interval: "5m", maxAge: "1h", dryRun: false }
```

:::info `served` vs `s3`
`served` keeps the source tarball on vibeD's PVC and serves it over the in-cluster Service URL — **dev only**. Once a restrictive sandbox NetworkPolicy is in place (production), sandboxes have no cluster DNS or cluster-internal egress, so the agent can only pull from a **pre-signed `s3` URL**. Use `s3` (S3 or MinIO) in production.
:::

## Helm values (topology)

```yaml
namespaces:
  apps: vibed-apps             # VibedApps + claims + templates + warm pools (one namespace)

runtime:
  defaultClass: kata-qemu      # kata-qemu (dev / no nested virt) | kata-fc (KVM)
  installRuntimeClass: false   # let Helm own the RuntimeClass
  nodeSelector: {}             # e.g. { vibed.dev/sandbox-node: "true" }
  sandboxNetworkPolicy: Managed  # Managed (agent-sandbox owns it) | Unmanaged (vibeD owns it)

networkPolicy:
  enabled: false               # vibeD-owned sandbox NetworkPolicy (prod: pair with Unmanaged)

controller:
  domain: vibed.example.com    # DNS suffix for app URLs
  urlScheme: https             # http in dev
  urlPort: ""                  # optional port appended to app URLs; empty in dev (kind bridges host:80 → Caddy)

router: { enabled: true }
caddy:
  enabled: true
  tls:
    dns01: { enabled: false, provider: cloudflare, tokenSecret: "" }  # wildcard TLS in prod

workerd: { enabled: false, replicas: 3 }   # fast-lane V8 isolates

warmPools:                     # one SandboxTemplate + SandboxWarmPool per template
  node-24:      { enabled: true, lane: general, image: "...", replicas: 50 }
  python-313:   { enabled: true, lane: general, image: "...", replicas: 50 }
  go-123:       { enabled: true, lane: general, image: "...", replicas: 20 }
  base-al2023:  { enabled: true, lane: general, image: "...", replicas: 30 }
  static-nginx: { enabled: true, lane: fast,    image: "...", replicas: 30 }
```

See `deploy/helm/vibed/values.yaml` for the fully-documented defaults and `values-kind.yaml` for the dev overlay.

## Environment overrides

Most app-config fields have a `VIBED_*` environment override (e.g. `VIBED_SERVER_TRANSPORT`, `VIBED_AUTH_ENABLED`, `VIBED_TRACING_ENDPOINT`). `KUBECONFIG` selects the cluster when running outside it. The controller takes flags (`--vibed-domain`, `--app-url-scheme`, `--app-url-port`, `--pool-namespace`) which the chart wires from the values above.
