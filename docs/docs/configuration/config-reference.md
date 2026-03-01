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

auth:
  enabled: false              # Enable authentication for /mcp/ and /api/ endpoints
  mode: "apikey"              # apikey | oauth
  apiKeys:
    - key: "env:VIBED_API_KEY"  # Resolve from environment variable
      name: "default"
    - key: "vibed_sk_..."     # Literal API key value
      name: "ci-pipeline"
      scopes: ["deploy"]
  tls:
    enabled: false            # Enable HTTPS
    certFile: ""              # Path to TLS certificate file
    keyFile: ""               # Path to TLS private key file
    autoTLS: false            # Auto-generate self-signed cert (dev only)

deployment:
  preferredTarget: "auto"     # auto | knative | kubernetes | wasmcloud
  namespace: "default"        # K8s namespace for deployed artifacts

builder:
  image: "paketobuildpacks/builder-jammy-base:latest"
  pullPolicy: "if-not-present"  # always | never | if-not-present
  containerRuntime: "auto"      # auto | docker | podman

storage:
  backend: "local"            # local | github
  local:
    basePath: "/data/vibed/artifacts"
  github:
    owner: ""
    repo: ""
    branch: "main"

registry:
  enabled: false
  url: ""                     # e.g. "ghcr.io/myorg/vibed"

store:
  backend: "memory"           # memory | configmap
  configmap:
    name: "vibed-artifacts"
    namespace: "vibed-system"

kubernetes:
  kubeconfig: ""              # Empty = in-cluster config
  context: ""                 # Specific kubeconfig context

knative:
  domainSuffix: "127.0.0.1.sslip.io"
  ingressClass: "kourier.ingress.networking.knative.dev"
```

## Environment Variables

Every config field has an environment variable override:

| Variable | Config Path | Example |
|----------|-------------|---------|
| `VIBED_SERVER_TRANSPORT` | `server.transport` | `http` |
| `VIBED_SERVER_HTTP_ADDR` | `server.httpAddr` | `:9090` |
| `VIBED_DEPLOYMENT_PREFERRED_TARGET` | `deployment.preferredTarget` | `knative` |
| `VIBED_DEPLOYMENT_NAMESPACE` | `deployment.namespace` | `apps` |
| `VIBED_BUILDER_IMAGE` | `builder.image` | `paketobuildpacks/...` |
| `VIBED_STORAGE_BACKEND` | `storage.backend` | `github` |
| `VIBED_STORAGE_LOCAL_BASE_PATH` | `storage.local.basePath` | `/data` |
| `VIBED_REGISTRY_ENABLED` | `registry.enabled` | `true` |
| `VIBED_REGISTRY_URL` | `registry.url` | `ghcr.io/...` |
| `VIBED_STORE_BACKEND` | `store.backend` | `configmap` |
| `VIBED_AUTH_ENABLED` | `auth.enabled` | `true` |
| `VIBED_AUTH_MODE` | `auth.mode` | `apikey` |
| `VIBED_AUTH_API_KEY` | (appends API key) | `vibed_sk_...` |
| `VIBED_TLS_ENABLED` | `auth.tls.enabled` | `true` |
| `VIBED_TLS_CERT_FILE` | `auth.tls.certFile` | `/etc/tls/tls.crt` |
| `VIBED_TLS_KEY_FILE` | `auth.tls.keyFile` | `/etc/tls/tls.key` |
| `VIBED_TLS_AUTO` | `auth.tls.autoTLS` | `true` |
| `KUBECONFIG` | `kubernetes.kubeconfig` | `~/.kube/config` |
| `GITHUB_TOKEN` | (GitHub auth) | `ghp_...` |
