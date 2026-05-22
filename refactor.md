## 1. Context and Non-Negotiables

### 1.1 What VibeD is

VibeD is an open-source platform that lets an AI agent (Claude Desktop, Cursor, Goose, or any MCP client) deploy a small web application **in under 10 seconds** and get back a public HTTPS URL. The target user is an enterprise employee who used GenAI to vibe-code a small tool for themselves; VibeD captures that app into a centrally governed sandbox instead of letting it live on the user's desktop.

### 1.2 Hard requirements

1. **Deploy latency**: p50 < 2s, p99 < 10s, from `POST /v1/deploy` to a working URL.
2. **Runtime coverage**: static HTML/JS, Node.js, Python, Go, plus arbitrary OCI images for the long tail.
3. **Isolation**: hardware-grade for untrusted code (microVM), with a faster V8/WASM lane for trusted-language workloads.
4. **Self-hostable on Kubernetes**: ships as a Helm chart, runs on EKS/GKE/AKS or on-prem.
5. **No container build on the deploy hot path. Ever.** Builds happen asynchronously to refresh template pools.
6. **License**: Apache-2.0 for the project. All dependencies must be OSI-approved.

### 1.3 Architectural rules Claude Code MUST follow

- **Two tiers, one API**. The HTTP API is uniform; the classifier picks the lane. Never expose "which runtime" as a user-facing parameter except as an optional override.
- **Warm pools, not on-demand boots**. Every supported template has a `SandboxWarmPool` resource sized to expected concurrency.
- **Code injection over image build**. User source is a tarball that gets extracted into a long-lived template at start time. The deploy path does not call `docker build`, `buildah`, `kaniko`, `nixpacks`, or any image builder.
- **Snapshot-restore for redeploy**. After first successful boot of an app, snapshot it; future deploys of the same `app-id` restore from snapshot + diff.
- **Built on `kubernetes-sigs/agent-sandbox`**. Do not invent CRDs that duplicate `Sandbox`, `SandboxTemplate`, `SandboxClaim`, or `SandboxWarmPool`. Wrap them.
- **Caddy with a wildcard DNS-01 cert** for `*.vibed.example.com`. Never request per-app ACME certs on the default subdomain.
- **Stateless control plane**. All durable state in Postgres + object storage. The API server is horizontally scalable and crash-safe.

### 1.4 What VibeD is NOT

- Not a Knative replacement for long-running production services. It optimizes for *deploy speed* of *small, ephemeral, employee-generated apps*.
- Not a code editor or IDE.
- Not a build service. Builds are an internal implementation detail of warm-pool maintenance, never a synchronous user operation.
- Not a multi-region orchestrator in v1. Single-region first.

### 1.5 Glossary

- **App**: a user-deployed unit (one source tree, one URL).
- **Template**: a pre-baked rootfs + runtime (e.g. `node-24`, `python-313`, `static-nginx`).
- **Sandbox**: a running instance of an app inside an isolation boundary (Kata+Firecracker microVM, or workerd isolate).
- **Warm pool**: a set of pre-booted sandboxes of a given template, idle and ready to claim.
- **Claim**: the act of binding an empty warm-pool sandbox to a specific app at deploy time.
- **Lane**: the runtime track — `fast` (workerd/Spin) or `general` (Kata+Firecracker).


## 3. Core CRD: `VibedApp`

VibeD owns one CRD. Everything else reuses `agent-sandbox` CRDs.

`crds/vibedapp_crd.yaml`:

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: vibedapps.vibed.dev
spec:
  group: vibed.dev
  names:
    kind: VibedApp
    plural: vibedapps
    singular: vibedapp
    shortNames: [vapp]
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          required: [spec]
          properties:
            spec:
              type: object
              required: [owner, source, runtime]
              properties:
                owner:
                  type: string
                  description: "Authenticated user identity (email or subject)."
                source:
                  type: object
                  properties:
                    tarballRef:
                      type: string
                      description: "S3 URL to the gzipped tarball of the app source."
                    gitRef:
                      type: string
                      description: "Optional: git URL + commit instead of tarball."
                runtime:
                  type: object
                  required: [lane]
                  properties:
                    lane:
                      type: string
                      enum: [fast, general]
                    template:
                      type: string
                      description: "e.g. node-24, python-313, static-nginx."
                    entrypoint:
                      type: string
                      description: "Optional override; otherwise autodetected."
                    env:
                      type: array
                      items:
                        type: object
                        properties:
                          name: { type: string }
                          value: { type: string }
                resources:
                  type: object
                  properties:
                    cpu:    { type: string, default: "500m" }
                    memory: { type: string, default: "256Mi" }
                ttl:
                  type: string
                  description: "Idle TTL before suspend-to-snapshot. Default 30m."
                  default: "30m"
            status:
              type: object
              properties:
                phase:
                  type: string
                  enum: [Pending, Claiming, Starting, Ready, Suspended, Failed]
                url:
                  type: string
                sandboxRef:
                  type: string
                  description: "Reference to agent-sandbox Sandbox CR."
                lastDeployedAt:
                  type: string
                  format: date-time
                snapshotRef:
                  type: string
                  description: "S3 URL of latest Firecracker snapshot."
                conditions:
                  type: array
                  items:
                    type: object
                    properties:
                      type:               { type: string }
                      status:             { type: string }
                      lastTransitionTime: { type: string, format: date-time }
                      reason:             { type: string }
                      message:            { type: string }
      additionalPrinterColumns:
        - { name: Phase,   type: string, jsonPath: .status.phase }
        - { name: URL,     type: string, jsonPath: .status.url }
        - { name: Runtime, type: string, jsonPath: .spec.runtime.template }
        - { name: Age,     type: date,   jsonPath: .metadata.creationTimestamp }
      subresources:
        status: {}
```

The `vibed-controller` reconciles `VibedApp` → `agent-sandbox` `SandboxClaim` + Caddy route entry.

---

## 4. HTTP API

`api/openapi.yaml` is the source of truth. Summarized endpoints:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/deploy` | Deploy a new app. Body: multipart with `source.tar.gz` + JSON metadata. Returns `{ app_id, url, status_url }`. **Must return within 10s** with status `Ready` or a streaming progress channel. |
| `GET` | `/v1/apps/{app_id}` | Get app status, URL, last-deployed timestamp. |
| `GET` | `/v1/apps` | List apps for the authenticated user. |
| `DELETE` | `/v1/apps/{app_id}` | Tear down. |
| `POST` | `/v1/apps/{app_id}/redeploy` | Redeploy with new source. Uses snapshot-restore if available. |
| `POST` | `/v1/apps/{app_id}/suspend` | Suspend to snapshot (frees compute). |
| `GET` | `/v1/apps/{app_id}/logs` | Stream logs (SSE). |
| `GET` | `/v1/templates` | List available runtime templates. |
| `GET` | `/healthz`, `/readyz` | K8s probes. |
| `GET` | `/metrics` | Prometheus. |

### Auth

- Default: bearer tokens issued by `vibed-api` after OIDC login. Enterprises plug their IdP via standard OIDC config in Helm values.
- Service-to-service inside the cluster: mTLS via cert-manager.

### Latency contract

`POST /v1/deploy` MUST respond within 10s with one of:
- `200 OK` + final URL (happy path).
- `202 Accepted` + `status_url` if classification picks a slow path (e.g., a runtime not in the warm pool requires async template build). The API must surface this case explicitly — it is a degraded mode, not the default.

---

## 5. Component Specifications

### 5.1 `vibed-api` (Go)

Stateless HTTP server. Responsibilities:

1. AuthN/AuthZ (OIDC bearer).
2. Accept multipart upload, stream tarball to S3 (or MinIO). Reject > 50 MB.
3. Run classifier (section 5.2) to pick lane and template.
4. Create a `VibedApp` CR.
5. Block on watching the CR's `.status.phase` until `Ready` or timeout (10s).
6. Return URL.

Use `controller-runtime`'s client for K8s, `pgx` for Postgres, `aws-sdk-go-v2` for S3.

### 5.2 Classifier (Go, in `internal/classifier`)

Pure function: `Classify(tarball io.Reader) (Lane, Template, error)`.

Decision tree (run in order, first match wins):

1. **No server-side code** (only `.html`, `.css`, `.js`, `.svg`, `.png`, etc. — no `package.json`, no `requirements.txt`, no compiled binary, no Dockerfile): → `fast` lane, `static-nginx` template.
2. **`package.json` with only browser deps + a `build` script producing `dist/` or `build/`**: → build asynchronously, serve from `static-nginx`. **Until built**, serve via `fast` lane with workerd running a stub that returns 503 with a retry hint. (See 5.7 on async build.)
3. **`worker.js` / `worker.ts` / `wrangler.toml`** OR small Node app < 1 MB with no native deps: → `fast` lane, workerd.
4. **`spin.toml` present**: → `fast` lane, Spin/SpinKube.
5. **`package.json`** (Node app with deps): → `general` lane, `node-24` template.
6. **`requirements.txt` / `pyproject.toml`**: → `general` lane, `python-313`.
7. **`go.mod`**: → `general` lane, `go-123` (with pre-built `go` toolchain in template).
8. **`Dockerfile`** at root: → `general` lane, `base-al2023` template, run user's start command (Dockerfile is a hint, not built).
9. **Else**: → `general` lane, `base-al2023`, autodetect entrypoint via heuristics in `vibed-agent`.

Classifier MUST be deterministic and fast (< 50ms on a 50 MB tarball). It only reads file *names* and `package.json` top-level keys, never installs anything.

### 5.3 `vibed-controller` (Go)

Standard controller-runtime reconciler for `VibedApp`. State machine:

```
Pending → Claiming → Starting → Ready
                              ↘ Suspended (after idle TTL)
                              ↘ Failed
```

Reconcile loop:

1. **Pending → Claiming**: create a `SandboxClaim` (from `agent-sandbox`) referencing the right `SandboxWarmPool` (one per template).
2. **Claiming → Starting**: once `SandboxClaim` reports a bound `Sandbox`, get its pod IP, POST source tarball URL to `vibed-agent` (port 9000) inside the sandbox.
3. **Starting → Ready**: poll `vibed-agent`'s `/ready` endpoint (1s interval, 8s budget). On success, register route with `vibed-router`.
4. **Ready → Suspended**: after `ttl` of zero traffic (measured by router), call Firecracker snapshot via Kata's snapshot API, write snapshot to S3, scale down sandbox.
5. **On redeploy**: if `snapshotRef` exists, claim a warm pod that supports snapshot-restore and restore from S3.

### 5.4 `vibed-agent` (Go, statically linked, ~5 MB)

Runs as PID 1 inside every sandbox. Single binary. Built into every template image.

API (loopback only, port 9000):

- `POST /deploy` with body `{ source_url, env, entrypoint }`: download tarball from S3 (pre-signed URL), `tar -xzf` into `/workspace`, run autodetect or honor explicit `entrypoint`, fork the user process with proper env and stdio capture.
- `GET /ready`: returns 200 once user process has been listening on its declared port for ≥ 100ms.
- `GET /logs`: tails captured stdout/stderr.
- `POST /suspend`: flushes logs, signals readiness for snapshot.

Autodetect rules (when no explicit entrypoint):

1. `package.json` `scripts.start` → `npm start`.
2. `requirements.txt` + `app.py` → `python app.py`.
3. `requirements.txt` + `main.py` → `python main.py`.
4. `go.mod` + a built binary → run the binary.
5. `index.html` at root → not applicable, this should have been static lane.
6. Else: read `vibed.toml` if present; otherwise fail with a clear error.

Port discovery: agent injects `PORT=8080` env and watches `netstat`/`/proc/net/tcp` to confirm the user process is listening before reporting ready.

### 5.5 `vibed-router` (Go)

Generates Caddy config and serves the `ask` endpoint.

- Single Caddy instance (or Deployment of N replicas behind a Service) with a wildcard cert for `*.vibed.example.com` obtained via DNS-01 (Cloudflare, Route53, or any cert-manager-supported provider).
- Routes are stored in a small KV (etcd or Postgres). `vibed-router` writes a Caddyfile (or uses Caddy's admin API) on every `VibedApp` status change.
- The `ask` endpoint at `/_vibed/cert-allowed` is used only for BYO custom domains and verifies the host is owned by the app.

Caddyfile fragment (templated):

```
*.vibed.example.com {
    tls {
        dns cloudflare {env.CF_API_TOKEN}
    }

    @app header_regexp host Host ^(?P<id>[a-z0-9]{12})\.vibed\.example\.com$
    reverse_proxy @app {http.regexp.host.id}.vibed-apps.svc.cluster.local:8080
}
```

### 5.6 Fast lane: `workerd` loader

Run `workerd` as a StatefulSet (sharded by `hash(app_id) % N`). Each replica holds many isolates.

The loader is a Go sidecar that:
1. Receives "deploy script" RPC from `vibed-controller`.
2. Writes the user script to a known path.
3. Renders a fresh `config.capnp` listing all current scripts as separate services.
4. Signals workerd to reload (SIGHUP or admin socket).

For BYO Spin apps: SpinKube's `SpinApp` CRD; one CR per app. No loader needed beyond that.

### 5.7 Async template builder

When a user app needs a dependency combination not in the warm pool (e.g. Node 22 + `sharp` + Python 3.11 for some `node-gyp` build), the deploy still completes via `base-al2023` + on-demand `npm install`. In parallel, a `vibed-builder` job:

1. Builds a new template that includes those dependencies.
2. Publishes it as a new `SandboxTemplate`.
3. Updates the corresponding `SandboxWarmPool` to use the new template.

Next deploy of similar shape hits the fast path. **This builder never blocks user-visible deploys.**

### 5.8 `vibed-mcp` (TypeScript)

A Model Context Protocol server. Single tool exposed:

```ts
tools: [{
  name: "deploy_app",
  description: "Deploy a web application to VibeD and return a public URL.",
  inputSchema: {
    type: "object",
    properties: {
      source_path: { type: "string", description: "Absolute path to the app's source directory." },
      name:        { type: "string", description: "Optional human-readable name." },
      env:         { type: "object", description: "Optional env vars." }
    },
    required: ["source_path"]
  }
}]
```

Behavior: tar+gzip the directory (respect `.vibedignore` and `.gitignore`), POST to `/v1/deploy`, stream progress as MCP notifications, return the URL.

### 5.9 `vibed` CLI (TypeScript)

Subcommands:
- `vibed login` — OIDC device-code flow.
- `vibed deploy [path]` — equivalent to MCP `deploy_app`.
- `vibed list` — list user's apps.
- `vibed logs <app-id>` — stream logs.
- `vibed delete <app-id>`
- `vibed redeploy <app-id> [path]`

---

## 6. Templates

Each template is a directory under `templates/` with:

- `Dockerfile` — used to build the template image. **Built by CI on template changes, NOT per deploy.** Resulting image is pushed to the configured registry.
- `entrypoint.sh` — copied into the image; invoked by `vibed-agent` after source is extracted.
- `template.yaml` — the `SandboxTemplate` + `SandboxWarmPool` manifests.

### 6.1 Minimum template set for v1

| Template | Base | Runtime | Default warm pool size |
|---|---|---|---|
| `node-24` | `node:24-bookworm-slim` | Node 24 + npm + pnpm + common build tools | 50 |
| `node-22` | `node:22-bookworm-slim` | Node 22 (LTS) | 50 |
| `python-313` | `python:3.13-slim` | Python 3.13 + pip + uv | 50 |
| `go-123` | `golang:1.23` | Go 1.23 toolchain + common libs | 20 |
| `bun-1` | `oven/bun:1` | Bun | 20 |
| `deno-2` | `denoland/deno:2` | Deno | 20 |
| `static-nginx` | `nginx:alpine` | nginx serving `/workspace` | 30 |
| `base-al2023` | `amazonlinux:2023` | sudo + dnf + Node + Python + Go (kitchen-sink) | 30 |

Warm pool sizes are starting values in `values.yaml`, autoscaled by `vibed-controller` based on claim rate over a 60s window.

### 6.2 Template image rules

- Each image MUST embed `vibed-agent` at `/usr/local/bin/vibed-agent` and run it as PID 1.
- Each image MUST include a populated dependency cache (`/opt/npm-cache`, `/opt/pip-cache`, `/root/.cache/go-build`) for the most common 500 packages in that ecosystem. Rebuild monthly via CI.
- Images are kept small (< 500 MB compressed). The kitchen-sink `base-al2023` may go to 1.5 GB.

---

## 7. Kubernetes Topology

### 7.1 Namespaces

- `vibed-system` — control plane: `vibed-api`, `vibed-controller`, `vibed-router`, Postgres, Caddy.
- `vibed-pools` — `SandboxWarmPool` resources and warm sandboxes.
- `vibed-apps` — claimed sandboxes serving traffic. (`agent-sandbox` moves a pod here on claim.)

### 7.2 RuntimeClass

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: { name: kata-fc }
handler: kata-fc
scheduling:
  nodeSelector:
    vibed.dev/sandbox-node: "true"
```

A second `kata-qemu` RuntimeClass exists for clusters without nested virt / bare metal. The controller picks based on a `values.yaml` setting.

### 7.3 Node pool requirements

- Dedicated node pool labeled `vibed.dev/sandbox-node: "true"`.
- Kata + Firecracker requires KVM. On AWS use `*.metal` or `c6i.metal` etc.; on GCP use nested-virt-enabled images; on bare metal anywhere works.
- Each node runs containerd + `containerd-shim-kata-v2`.

### 7.4 Networking

- Cilium (or any CNI with `NetworkPolicy` support).
- Default-deny egress for `vibed-apps` namespace; allow-list outbound by hostname through an egress proxy (squid or Cloudflare gateway). This is where enterprise data-egress controls live.
- Sandbox pods have no access to cluster DNS for internal services.

---

## 8. Helm Chart

`helm/vibed/values.yaml` defaults:

```yaml
domain: vibed.example.com
tlsProvider: cloudflare           # cloudflare | route53 | acme-http01
oidc:
  issuer: ""
  clientId: ""
  clientSecret: ""
storage:
  s3:
    endpoint: ""                  # leave empty for AWS
    bucket:   vibed-sources
    region:   us-east-1
postgres:
  embedded: true                  # ships a small Postgres for dev; set false in prod
runtime:
  defaultClass: kata-fc           # kata-fc | kata-qemu
templates:
  node-24:        { warmPool: 50 }
  node-22:        { warmPool: 50 }
  python-313:     { warmPool: 50 }
  go-123:         { warmPool: 20 }
  bun-1:          { warmPool: 20 }
  deno-2:         { warmPool: 20 }
  static-nginx:   { warmPool: 30 }
  base-al2023:    { warmPool: 30 }
fastLane:
  workerd:
    replicas: 3
  spin:
    enabled: true
limits:
  appTarballMaxMB: 50
  appMemoryDefault: 256Mi
  appCpuDefault:    500m
  appTtl:           30m
```

`helm install vibed ./helm/vibed -n vibed-system --create-namespace` MUST produce a working system on any K8s 1.29+ cluster with a compatible node pool.

---

## 9. Observability and Operations

### 9.1 Required metrics (Prometheus, all `vibed_*` prefix)

- `vibed_deploy_duration_seconds` (histogram, labels: `lane`, `template`, `outcome`).
- `vibed_warm_pool_size` (gauge, label: `template`).
- `vibed_warm_pool_claims_total` (counter).
- `vibed_warm_pool_misses_total` (counter — when pool was empty and a fresh boot was needed).
- `vibed_sandbox_running` (gauge).
- `vibed_snapshot_restore_duration_seconds`.
- Standard HTTP server metrics for `vibed-api`.

### 9.2 SLOs (track from day one)

- p50 deploy latency: < 2s.
- p99 deploy latency: < 10s.
- Deploy success rate: > 99.5%.
- Warm pool miss rate: < 1%.

### 9.3 Logs and traces

- Structured JSON via `slog`.
- OpenTelemetry traces from `vibed-api` → `vibed-controller` → `vibed-agent`. Trace ID propagated through the `VibedApp` CR annotations.

---

## 10. Testing

### 10.1 Unit
- Classifier: table-driven tests covering each of the 9 decision rules with sample tarball file lists.
- Controller: envtest with fake `agent-sandbox` CRDs.
- Agent: tests against a real `/workspace` with sample apps.

### 10.2 E2E (`test/e2e/`)
- Spin up kind cluster with `agent-sandbox` + Kata + Caddy.
- Deploy each fixture in `test/fixtures/` (one per template).
- Assert: URL returned, URL responds 200, p99 < 10s over 100 deploys.
- Tear down.

### 10.3 Load (`test/load/`)
- k6 script: 100 concurrent users, each deploys a fresh app every 30s for 10 minutes.
- Pass criteria: p99 < 10s, warm-pool miss rate < 5%.

---

## 11. Build Order for Claude Code

Implement in this strict order. Each step ends with a working, committable artifact.

1. **Repo skeleton + Makefile + CI.** `make lint test` passes on an empty project. CI runs on GitHub Actions.
2. **`api/openapi.yaml`** complete, with generated Go types in `pkg/vibedapi/`.
3. **`VibedApp` CRD + envtest harness.**
4. **`vibed-agent`** standalone: a binary that, given a tarball URL and an entrypoint, downloads, extracts, and runs a process while exposing `/ready` and `/logs`. Testable in plain Docker.
5. **One template end-to-end: `node-24`.** Dockerfile, entrypoint, `SandboxTemplate`, `SandboxWarmPool`. Manually create a `Sandbox`, exec into it, prove `vibed-agent` works.
6. **`vibed-controller`** reconciling `VibedApp` → `SandboxClaim`. Deploy the fixture `hello-node` and watch it become `Ready`. No HTTP API yet.
7. **`vibed-router`** with wildcard DNS-01 Caddy. Route traffic to the running sandbox. Hit the URL.
8. **`vibed-api`** wiring it all together. `POST /v1/deploy` works end-to-end for `node-24`.
9. **Classifier.** Add `static-nginx` and `python-313` templates. Multi-template deploys work.
10. **Fast lane v1: `workerd` loader.** Static HTML deploys via fast lane. Measure latency improvement.
11. **Snapshot/restore for redeploy.**
12. **Idle TTL → suspend.**
13. **`vibed` CLI** (TypeScript).
14. **`vibed-mcp`** MCP server for Claude Desktop.
15. **Helm chart** packaging the whole thing.
16. **E2E tests** in CI on a kind cluster.
17. **Load tests** + SLO dashboards.
18. **Spin/SpinKube fast lane.**
19. **Async template builder.**
20. **Docs + ADRs + v0.1 release.**

Do not skip ahead. Each step must include tests for the code it introduces.

---

## 12. Out of Scope for v1

- Multi-region.
- GPU sandboxes.
- Persistent volumes per app (apps are ephemeral; for persistence, point them at the user's own DB).
- Built-in secrets manager (use external: Vault, AWS Secrets Manager, etc., referenced by Kubernetes `Secret`).
- Custom domain provisioning UI (API exists, UI later).
- Web UI / dashboard (CLI + MCP only in v1).

---

## 13. References (read these before starting)

- `kubernetes-sigs/agent-sandbox` — README, v0.2.1 release notes, the four CRDs.
- Kata Containers configuration with Firecracker hypervisor — `kata-containers/kata-containers` docs/how-to.
- Cloudflare `workerd` — `cloudflare/workerd` README and `config.capnp` schema.
- Fermyon Spin + SpinKube — `spinkube.dev` getting-started.
- Caddy on-demand TLS + DNS-01 — caddyserver.com/docs/automatic-https.
- Firecracker snapshot API — `firecracker-microvm/firecracker` SPECIFICATION.md.
- Lambda SnapStart for the snapshot-restore correctness pitfalls — AWS docs section "Uniqueness".
