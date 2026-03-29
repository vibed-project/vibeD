---
sidebar_position: 1
---

# Configuration Reference

vibeD is configured via a YAML file and environment variables.

## Config File

Default search paths: `./vibed.yaml`, `/etc/vibed/vibed.yaml`

```yaml
server:
  transport: "http"           # stdio | http | both
  httpAddr: ":8080"           # HTTP listen address
  logFormat: "text"           # text | json (structured JSON for log aggregation)
  logLevel: "info"            # debug | info | warn | error
  rateLimit:
    enabled: false            # Enable per-client HTTP rate limiting
    requestsPerSecond: 10     # Steady-state rate per client
    burst: 20                 # Max burst size per client

auth:
  enabled: false              # Enable authentication for /mcp/ and /api/ endpoints
  mode: "apikey"              # apikey | oauth
  apiKeys:
    - key: "env:VIBED_API_KEY"  # Resolve from environment variable
      name: "default"
    - key: "vibed_sk_..."     # Literal API key value
      name: "ci-pipeline"
      scopes: ["deploy"]
    - key: "env:ALICE_KEY"    # Per-user storage override
      name: "alice"
      storage:                # Optional: route this user's artifacts to a dedicated repo
        backend: "github"     # github | gitlab
        github:
          owner: "alice-org"
          repo: "alice-artifacts"
          token: "env:ALICE_GITHUB_TOKEN"
  tls:
    enabled: false            # Enable HTTPS
    certFile: ""              # Path to TLS certificate file
    keyFile: ""               # Path to TLS private key file
    autoTLS: false            # Auto-generate self-signed cert (dev only)

deployment:
  preferredTarget: "auto"     # auto | knative | kubernetes
  namespace: "default"        # K8s namespace for deployed artifacts

builder:
  engine: "buildah"             # Container image builder (Buildah via K8s Jobs)
  buildah:
    image: "quay.io/buildah/stable:latest"  # Buildah container image
    timeout: "10m"              # Build job timeout
    insecure: false             # Set true for HTTP registries (e.g. in-cluster)

storage:
  backend: "local"            # local | github | gitlab
  local:
    basePath: "/data/vibed/artifacts"
  github:
    owner: ""
    repo: ""
    branch: "main"
  gitlab:
    url: "https://gitlab.com" # GitLab instance URL
    projectID: 0              # GitLab project ID (required for gitlab backend)
    branch: "main"
    token: ""                 # Access token or "env:GITLAB_TOKEN" / "file:/path"

registry:
  enabled: false
  url: ""                     # e.g. "ghcr.io/myorg/vibed"
  insecure: false             # Use HTTP instead of HTTPS (for in-cluster registries)

store:
  backend: "sqlite"           # sqlite (default) | memory | configmap
  sqlite:
    path: "/data/vibed.db"    # SQLite database file path
  configmap:
    name: "vibed-artifacts"
    namespace: "vibed-system"

gc:
  enabled: true               # Enable resource garbage collector
  interval: "1h"              # How often GC runs
  maxAge: "24h"               # Age threshold for orphaned resources
  dryRun: false               # Log without deleting (for testing)

kubernetes:
  kubeconfig: ""              # Empty = in-cluster config
  context: ""                 # Specific kubeconfig context

knative:
  domainSuffix: "127.0.0.1.sslip.io"
  ingressClass: "kourier.ingress.networking.knative.dev"
  gatewayPort: 80             # External gateway port for URLs (0 or 80 = omitted from URLs)

tracing:
  enabled: false              # Enable OpenTelemetry distributed tracing
  endpoint: ""                # OTLP gRPC endpoint (e.g. "http://jaeger:4317"); empty = stdout
  sampleRate: 1.0             # Sampling rate 0.0-1.0 (1.0 = sample all traces)
```

## Environment Variables

Every config field has an environment variable override:

| Variable | Config Path | Example |
|----------|-------------|---------|
| `VIBED_SERVER_TRANSPORT` | `server.transport` | `http` |
| `VIBED_SERVER_HTTP_ADDR` | `server.httpAddr` | `:9090` |
| `VIBED_LOG_FORMAT` | `server.logFormat` | `json` |
| `VIBED_LOG_LEVEL` | `server.logLevel` | `debug` |
| `VIBED_DEPLOYMENT_PREFERRED_TARGET` | `deployment.preferredTarget` | `knative` |
| `VIBED_DEPLOYMENT_NAMESPACE` | `deployment.namespace` | `apps` |
| `VIBED_BUILDER_ENGINE` | `builder.engine` | `buildah` |
| `VIBED_BUILDER_BUILDAH_IMAGE` | `builder.buildah.image` | `quay.io/buildah/stable:latest` |
| `VIBED_BUILDER_BUILDAH_INSECURE` | `builder.buildah.insecure` | `true` |
| `VIBED_STORAGE_BACKEND` | `storage.backend` | `github` or `gitlab` |
| `VIBED_STORAGE_LOCAL_BASE_PATH` | `storage.local.basePath` | `/data` |
| `VIBED_STORAGE_GITHUB_OWNER` | `storage.github.owner` | `myorg` |
| `VIBED_STORAGE_GITHUB_REPO` | `storage.github.repo` | `vibed-artifacts` |
| `VIBED_REGISTRY_ENABLED` | `registry.enabled` | `true` |
| `VIBED_REGISTRY_URL` | `registry.url` | `ghcr.io/...` |
| `VIBED_STORE_BACKEND` | `store.backend` | `sqlite` |
| `VIBED_STORE_SQLITE_PATH` | `store.sqlite.path` | `/data/vibed.db` |
| `VIBED_GC_ENABLED` | `gc.enabled` | `true` |
| `VIBED_GC_INTERVAL` | `gc.interval` | `1h` |
| `VIBED_GC_MAX_AGE` | `gc.maxAge` | `24h` |
| `VIBED_GC_DRY_RUN` | `gc.dryRun` | `true` |
| `VIBED_AUTH_ENABLED` | `auth.enabled` | `true` |
| `VIBED_AUTH_MODE` | `auth.mode` | `apikey` |
| `VIBED_AUTH_API_KEY` | (appends API key) | `vibed_sk_...` |
| `VIBED_TLS_ENABLED` | `auth.tls.enabled` | `true` |
| `VIBED_TLS_CERT_FILE` | `auth.tls.certFile` | `/etc/tls/tls.crt` |
| `VIBED_TLS_KEY_FILE` | `auth.tls.keyFile` | `/etc/tls/tls.key` |
| `VIBED_TLS_AUTO` | `auth.tls.autoTLS` | `true` |
| `VIBED_RATE_LIMIT_ENABLED` | `server.rateLimit.enabled` | `true` |
| `VIBED_RATE_LIMIT_RPS` | `server.rateLimit.requestsPerSecond` | `10` |
| `VIBED_RATE_LIMIT_BURST` | `server.rateLimit.burst` | `20` |
| `VIBED_REGISTRY_INSECURE` | `registry.insecure` | `true` |
| `VIBED_KNATIVE_GATEWAY_PORT` | `knative.gatewayPort` | `80` |
| `VIBED_TRACING_ENABLED` | `tracing.enabled` | `true` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `tracing.endpoint` (also enables tracing) | `http://jaeger:4317` |
| `VIBED_TRACING_ENDPOINT` | `tracing.endpoint` | `http://tempo:4317` |
| `VIBED_TRACING_SAMPLE_RATE` | `tracing.sampleRate` | `0.1` |
| `VIBED_AUTH_OIDC_ISSUER` | `auth.oidc.issuer` | `https://accounts.google.com` |
| `VIBED_AUTH_OIDC_AUDIENCE` | `auth.oidc.audience` | `vibed` |
| `VIBED_AUTH_OIDC_ADMIN_ROLE` | `auth.oidc.adminRole` | `vibed-admin` |
| `KUBECONFIG` | `kubernetes.kubeconfig` | `~/.kube/config` |
| `GITHUB_TOKEN` | (GitHub storage auth) | `ghp_...` |
| `GITLAB_TOKEN` | (GitLab storage auth) | `glpat-...` |
