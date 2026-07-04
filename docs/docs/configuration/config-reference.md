---
sidebar_position: 1
---

# Configuration Reference

There are two layers of configuration:

1. **Application config** (`vibed.yaml`) — runtime settings for the `vibed` server. In Kubernetes this is rendered into a ConfigMap by the Helm chart and mounted at `/etc/vibed/vibed.yaml`.
2. **Helm values** — cluster topology: namespaces, RuntimeClass, NetworkPolicy, the controller/router/caddy components, and the warm pools.

You normally set everything through Helm values; the chart renders the app config for you.

Config is loaded from a file, then **`VIBED_`-prefixed environment variables override individual keys**, then the result is validated. A missing config file is not an error — the server starts on defaults. Every field maps to a key defined in `internal/config/config.go`; the tables below are grouped by top-level section in that struct.

## Application config (`vibed.yaml`)

```yaml
organization:
  name: ""                     # display name, e.g. "Acme Corp"

server:
  transport: "stdio"           # stdio | http | both
  httpAddr: ":8080"
  baseURL: ""                  # public URL for share-link generation
  logFormat: "text"            # text | json
  logLevel: "info"             # debug | info | warn | error
  rateLimit: { enabled: false, requestsPerSecond: 10, burst: 20 }

auth:
  enabled: false               # enable before exposing the API
  mode: "apikey"               # apikey | oauth | oidc
  apiKeys: []                  # see auth section below
  # oidc: { issuer, audience, usernameClaim, roleClaim, adminRole, ... }
  # tls:  { enabled, certFile, keyFile, autoTLS }
  # options: {}                # extra keys read by out-of-tree auth providers

deployment:
  preferredTarget: "auto"      # auto | kubernetes
  namespace: "default"
  appsNamespace: "vibed-apps"  # where /v1 creates VibedApps + warm pools live
  readyTimeout: "10m"          # how long a deploy waits for Ready before failing

storage:
  backend: "local"             # local | github | gitlab
  local:  { basePath: "/data/vibed/artifacts" }
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

registry:
  enabled: false
  url: ""
  insecure: false

store:
  backend: "sqlite"            # sqlite | memory | configmap
  sqlite:    { path: "/data/vibed.db" }
  configmap: { name: "vibed-artifacts", namespace: "vibed-system" }
  # options: {}                # settings for out-of-tree store backends

kubernetes:
  kubeconfig: ""               # empty -> in-cluster / KUBECONFIG env
  context: ""

limits:
  maxTotalFileSize: 52428800   # 50 MiB
  maxFileCount: 500
  maxLogLines: 10000
  maxConcurrentLogStreamsPerUser: 10

quotas:
  enabled: false
  maxAppsPerOwner: 0           # 0 = unlimited
  maxAppsPerDepartment: 0      # 0 = unlimited
  perDepartment: {}            # override the department ceiling by name

audit:
  failClosed: false            # reject a mutating action if its audit write fails

gc:
  enabled: true
  interval: "1h"
  maxAge: "24h"
  dryRun: false

tracing:
  enabled: false
  endpoint: ""                 # OTLP gRPC endpoint; empty -> stdout
  sampleRate: 1.0
```

:::info `served` vs `s3`
`served` keeps the source tarball on vibeD's PVC and serves it over the in-cluster Service URL — **dev only**. Once a restrictive sandbox NetworkPolicy is in place (production), sandboxes have no cluster DNS or cluster-internal egress, so the agent can only pull from a **pre-signed `s3` URL**. Use `s3` (S3 or MinIO) in production.
:::

## `organization`

| Key    | Type   | Default | Description                                        |
| ------ | ------ | ------- | -------------------------------------------------- |
| `name` | string | `""`    | Organization display name, surfaced in the dashboard. |

## `server`

| Key                            | Type    | Default   | Description                                                                      |
| ------------------------------ | ------- | --------- | -------------------------------------------------------------------------------- |
| `transport`                    | string  | `stdio`   | `stdio`, `http`, or `both`. Validated — any other value fails startup.           |
| `httpAddr`                     | string  | `:8080`   | Listen address for the HTTP transport (`/v1` API, dashboard, streamable MCP).    |
| `baseURL`                      | string  | `""`      | Public-facing base URL used to generate share links, e.g. `http://localhost:8080`. |
| `logFormat`                    | string  | `text`    | `text` or `json`.                                                                |
| `logLevel`                     | string  | `info`    | `debug`, `info`, `warn`, or `error`.                                             |
| `rateLimit.enabled`            | bool    | `false`   | Enable per-client HTTP rate limiting.                                            |
| `rateLimit.requestsPerSecond`  | float   | `10`      | Steady-state rate per client.                                                    |
| `rateLimit.burst`              | int     | `20`      | Max burst size per client.                                                       |

## `auth`

Governs authentication and TLS termination. When `enabled` is `false` the API is open — enable it before exposing vibeD. See [Authentication](authentication.md) for a full walkthrough.

| Key       | Type              | Default | Description                                                                             |
| --------- | ----------------- | ------- | --------------------------------------------------------------------------------------- |
| `enabled` | bool              | `false` | Turn authentication on.                                                                  |
| `mode`    | string            | `""`    | `apikey`, `oauth`, or `oidc`. Out-of-tree providers may register additional modes.      |
| `apiKeys` | list              | `[]`    | Configured API keys (see below). At least one is required when `mode` is `apikey`/`oauth`/empty. |
| `oidc`    | object            | —       | OIDC settings (see below).                                                              |
| `tls`     | object            | —       | TLS termination (see below).                                                            |
| `options` | map[string]string | `{}`    | Generic bag read by out-of-tree auth providers; core modes ignore it, so a new mode needs no schema change. |

### `auth.apiKeys[]`

| Key          | Type   | Default  | Description                                                              |
| ------------ | ------ | -------- | ----------------------------------------------------------------------- |
| `key`        | string | —        | Token value, or `env:VAR_NAME` to read it from the environment.         |
| `name`       | string | —        | Human-readable name; used as the caller's `UserID`.                     |
| `scopes`     | list   | `[]`     | Allowed scopes; empty means all.                                        |
| `role`       | string | `user`   | `admin` or `user`.                                                      |
| `department` | string | `""`     | Auto-assign the user to this department on first use.                   |
| `storage`    | object | —        | Optional per-user storage override (`github` or `gitlab`, see [Storage](storage.md)). |

### `auth.oidc`

| Key               | Type   | Default                | Description                                                          |
| ----------------- | ------ | ---------------------- | ------------------------------------------------------------------- |
| `issuer`          | string | `""`                   | OIDC issuer URL. Required when `mode` is `oidc`.                     |
| `audience`        | string | `""`                   | Expected `aud` claim. Required when `mode` is `oidc` (blocks cross-app token reuse). |
| `usernameClaim`   | string | `preferred_username`   | JWT claim for the username.                                         |
| `emailClaim`      | string | `email`                | JWT claim for the email.                                            |
| `roleClaim`       | string | `realm_access.roles`   | JWT claim path for roles.                                           |
| `adminRole`       | string | `vibed-admin`          | Role value that maps to a vibeD admin.                              |
| `departmentClaim` | string | `""`                   | JWT claim carrying the department name.                            |
| `scopes`          | list   | `["openid","profile"]` | Scopes to advertise.                                               |

### `auth.tls`

| Key        | Type   | Default | Description                                                                 |
| ---------- | ------ | ------- | --------------------------------------------------------------------------- |
| `enabled`  | bool   | `false` | Terminate TLS in `vibed`.                                                    |
| `certFile` | string | `""`    | Path to the TLS certificate.                                                |
| `keyFile`  | string | `""`    | Path to the TLS private key.                                                |
| `autoTLS`  | bool   | `false` | Generate a self-signed cert for dev. When `enabled`, either `certFile`+`keyFile` or `autoTLS` is required. |

## `deployment`

| Key               | Type     | Default      | Description                                                                                   |
| ----------------- | -------- | ------------ | --------------------------------------------------------------------------------------------- |
| `preferredTarget` | string   | `auto`       | `auto` or `kubernetes`.                                                                        |
| `namespace`       | string   | `default`    | Default working namespace for the control plane.                                              |
| `appsNamespace`   | string   | `vibed-apps` | Namespace where `/v1` creates `VibedApp` CRs. Must match where the warm pools live — agent-sandbox requires a `SandboxClaim` co-located with its `SandboxTemplate`. |
| `readyTimeout`    | duration | `10m`        | How long deployers wait for a workload to become `Ready` before failing the deploy.           |

## `storage`

The file-tree artifact store used by the legacy MCP build path. Distinct from `storage.tarball`, which is the source-blob store for the `/v1/deploy` path. See [Storage](storage.md).

| Key       | Type   | Default | Description                                                          |
| --------- | ------ | ------- | ------------------------------------------------------------------- |
| `backend` | string | `local` | `local`, `github`, or `gitlab`. Validated at startup.               |
| `local`   | object | —       | `basePath` (default `/data/vibed/artifacts`).                       |
| `github`  | object | —       | `owner`, `repo`, `branch` (default `main`), `tokenFile`. `owner` and `repo` are required when `backend` is `github`. |
| `gitlab`  | object | —       | `url` (default `https://gitlab.com`), `projectID`, `branch` (default `main`), `token`. `projectID` is required when `backend` is `gitlab`. |
| `tarball` | object | —       | Source-blob store for `/v1/deploy` (see below).                     |

Tokens in `github.tokenFile` / `gitlab.token` support `env:VAR` and `file:PATH` indirection.

### `storage.tarball`

Selects how `/v1/deploy` persists the uploaded source tarball so `vibed-agent` can pull it.

| Key       | Type   | Default  | Description                                                     |
| --------- | ------ | -------- | -------------------------------------------------------------- |
| `backend` | string | `served` | `served` (no extra infra) or `s3`.                             |
| `served`  | object | —        | `basePath` (default `/data/vibed/sources`; should be a PVC), `publicBaseURL` (in-cluster base the agent dials; the store appends `/internal/sources/<id>.tar.gz`). |
| `s3`      | object | —        | `endpoint` (empty for AWS, set for MinIO), `bucket`, `region`, `accessKey`, `secretKey`, `presignTTL` (GET URL validity, default `15m`). |

## `registry`

Optional OCI image registry used by paths that reference images.

| Key        | Type   | Default | Description                                                              |
| ---------- | ------ | ------- | ----------------------------------------------------------------------- |
| `enabled`  | bool   | `false` | Enable registry integration. When `true`, `url` is required.            |
| `url`      | string | `""`    | Registry base URL.                                                      |
| `insecure` | bool   | `false` | Use HTTP instead of HTTPS. See [Registry](registry.md).                 |

## `store`

The state store for control-plane objects (the control plane is stateless; all durable state lives here).

| Key         | Type              | Default  | Description                                                                                  |
| ----------- | ----------------- | -------- | ------------------------------------------------------------------------------------------- |
| `backend`   | string            | `sqlite` | `sqlite`, `memory`, or `configmap`. Validated at startup.                                    |
| `sqlite`    | object            | —        | `path` (default `/data/vibed.db`). Required when `backend` is `sqlite`.                      |
| `configmap` | object            | —        | `name` (default `vibed-artifacts`), `namespace` (default `vibed-system`).                   |
| `options`   | map[string]string | `{}`     | Passed verbatim to the backend factory. Core backends ignore it; an out-of-tree backend reads its own settings (DSN, pool size, …) from here, so adding a backend needs no schema change. See [Store backends](../extending/store-backends.md). |

## `kubernetes`

| Key          | Type   | Default | Description                                                                  |
| ------------ | ------ | ------- | ---------------------------------------------------------------------------- |
| `kubeconfig` | string | `""`    | Path to a kubeconfig. Empty uses in-cluster config; the `KUBECONFIG` env var fills this when set and the key is empty. |
| `context`    | string | `""`    | Kubeconfig context to select.                                                |

## `limits`

Guards on MCP tool inputs and log streaming.

| Key                              | Type | Default    | Description                                                                       |
| -------------------------------- | ---- | ---------- | -------------------------------------------------------------------------------- |
| `maxTotalFileSize`               | int  | `52428800` | Max total source size per deploy/update, in bytes (50 MiB).                       |
| `maxFileCount`                   | int  | `500`      | Max number of files per deploy/update.                                           |
| `maxLogLines`                    | int  | `10000`    | Max log lines returned per request.                                              |
| `maxConcurrentLogStreamsPerUser` | int  | `10`       | Max simultaneous `/v1/logs` SSE streams per authenticated user. `0` = unlimited. |

## `quotas`

Caps how many concurrent apps an owner or department may hold. Disabled by default; a new deploy is hard-gated when it would exceed either ceiling. Counts are over live `VibedApp`s (by the `vibed.dev/owner` and `vibed.dev/department` labels); redeploys of an existing app do not count. See [Quotas](quotas.md).

| Key                    | Type          | Default | Description                                                     |
| ---------------------- | ------------- | ------- | --------------------------------------------------------------- |
| `enabled`              | bool          | `false` | Enforce quotas.                                                 |
| `maxAppsPerOwner`      | int           | `0`     | Per-user ceiling. `0` = unlimited.                             |
| `maxAppsPerDepartment` | int           | `0`     | Per-department aggregate ceiling. `0` = unlimited.            |
| `perDepartment`        | map[string]int | `{}`    | Override the department ceiling by department name.           |

## `audit`

| Key          | Type | Default | Description                                                                                     |
| ------------ | ---- | ------- | ---------------------------------------------------------------------------------------------- |
| `failClosed` | bool | `false` | When `true`, a mutating action (deploy/delete/rollback/suspend) whose audit write cannot be persisted is rejected — preferring availability loss over an untraceable change. The default fails open so dev clusters and the default install don't block deploys; production values flip this to `true`. See [Audit log](audit-log.md). |

## `gc`

The resource garbage collector, which reaps orphaned resources.

| Key        | Type     | Default | Description                                                       |
| ---------- | -------- | ------- | ---------------------------------------------------------------- |
| `enabled`  | bool     | `true`  | Enable garbage collection.                                       |
| `interval` | duration | `1h`    | GC cycle interval. Must be a valid Go duration when `enabled`.   |
| `maxAge`   | duration | `24h`   | Age threshold for orphaned resources. Must be a valid duration.  |
| `dryRun`   | bool     | `false` | Log candidates without deleting.                                 |

## `tracing`

OpenTelemetry distributed tracing.

| Key          | Type   | Default | Description                                                        |
| ------------ | ------ | ------- | ------------------------------------------------------------------ |
| `enabled`    | bool   | `false` | Enable tracing.                                                    |
| `endpoint`   | string | `""`    | OTLP gRPC endpoint (e.g. `http://jaeger:4317`). Empty exports to stdout. |
| `sampleRate` | float  | `1.0`   | Sampling rate, `0.0`–`1.0`.                                        |

## Environment overrides

`VIBED_`-prefixed environment variables override the matching config key after the file is parsed, so you can inject secrets and per-environment settings without editing `vibed.yaml`. Only the keys below have overrides.

| Environment variable                 | Overrides                            |
| ------------------------------------ | ------------------------------------ |
| `VIBED_ORGANIZATION_NAME`            | `organization.name`                  |
| `VIBED_SERVER_TRANSPORT`             | `server.transport`                   |
| `VIBED_SERVER_HTTP_ADDR`             | `server.httpAddr`                    |
| `VIBED_SERVER_BASE_URL`              | `server.baseURL`                     |
| `VIBED_LOG_FORMAT`                   | `server.logFormat`                   |
| `VIBED_LOG_LEVEL`                    | `server.logLevel`                    |
| `VIBED_RATE_LIMIT_ENABLED`           | `server.rateLimit.enabled`           |
| `VIBED_RATE_LIMIT_RPS`               | `server.rateLimit.requestsPerSecond` |
| `VIBED_RATE_LIMIT_BURST`             | `server.rateLimit.burst`             |
| `VIBED_AUTH_ENABLED`                 | `auth.enabled`                       |
| `VIBED_AUTH_MODE`                    | `auth.mode`                          |
| `VIBED_AUTH_API_KEY`                 | appends a key and enables `apikey` auth |
| `VIBED_AUTH_OIDC_ISSUER`             | `auth.oidc.issuer`                   |
| `VIBED_AUTH_OIDC_AUDIENCE`           | `auth.oidc.audience`                 |
| `VIBED_AUTH_OIDC_ADMIN_ROLE`         | `auth.oidc.adminRole`                |
| `VIBED_TLS_ENABLED`                  | `auth.tls.enabled`                   |
| `VIBED_TLS_CERT_FILE`                | `auth.tls.certFile`                  |
| `VIBED_TLS_KEY_FILE`                 | `auth.tls.keyFile`                   |
| `VIBED_TLS_AUTO`                     | `auth.tls.autoTLS`                   |
| `VIBED_DEPLOYMENT_PREFERRED_TARGET`  | `deployment.preferredTarget`         |
| `VIBED_DEPLOYMENT_NAMESPACE`         | `deployment.namespace`               |
| `VIBED_STORAGE_BACKEND`              | `storage.backend`                    |
| `VIBED_STORAGE_LOCAL_BASE_PATH`      | `storage.local.basePath`             |
| `VIBED_STORAGE_GITHUB_OWNER`         | `storage.github.owner`               |
| `VIBED_STORAGE_GITHUB_REPO`          | `storage.github.repo`                |
| `VIBED_REGISTRY_ENABLED`             | `registry.enabled`                   |
| `VIBED_REGISTRY_URL`                 | `registry.url`                       |
| `VIBED_STORE_BACKEND`                | `store.backend`                      |
| `VIBED_STORE_SQLITE_PATH`            | `store.sqlite.path`                  |
| `KUBECONFIG`                         | `kubernetes.kubeconfig` (only if unset) |
| `VIBED_LIMITS_MAX_TOTAL_FILE_SIZE`   | `limits.maxTotalFileSize`            |
| `VIBED_LIMITS_MAX_FILE_COUNT`        | `limits.maxFileCount`                |
| `VIBED_LIMITS_MAX_LOG_LINES`         | `limits.maxLogLines`                 |
| `VIBED_GC_ENABLED`                   | `gc.enabled`                         |
| `VIBED_GC_INTERVAL`                  | `gc.interval`                        |
| `VIBED_GC_MAX_AGE`                   | `gc.maxAge`                          |
| `VIBED_GC_DRY_RUN`                   | `gc.dryRun`                          |
| `VIBED_TRACING_ENABLED`             | `tracing.enabled`                    |
| `VIBED_TRACING_ENDPOINT`             | `tracing.endpoint`                   |
| `VIBED_TRACING_SAMPLE_RATE`          | `tracing.sampleRate`                 |
| `OTEL_EXPORTER_OTLP_ENDPOINT`        | `tracing.endpoint` (and enables tracing) |

`KUBECONFIG` selects the cluster when running outside it, and only applies when `kubernetes.kubeconfig` is empty.

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

See `deploy/helm/vibed/values.yaml` for the fully-documented defaults and `values-kind.yaml` for the dev overlay. The controller takes flags (`--vibed-domain`, `--app-url-scheme`, `--app-url-port`, `--pool-namespace`) which the chart wires from the values above.

## Extending the config

Several sections carry an `options` map (or an equivalent seam) so an **out-of-tree Go module** can plug in a provider without changing this schema:

- `auth.options` / `auth.mode` — register a custom authentication mode. See [Auth providers](../extending/auth-providers.md).
- `store.options` / `store.backend` — register a custom state-store backend that reads its own DSN and tuning from `options`. See [Store backends](../extending/store-backends.md).

Feature availability is governed by an editions/feature-flag seam whose default enables **all** features; integrators building their own module can supply a different resolver. See [Secrets and features](../extending/secrets-and-features.md).
