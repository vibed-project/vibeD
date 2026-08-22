---
sidebar_position: 1
---

# Local Development Setup

vibeD develops on **Podman + Kind**. The dev install runs the full stack (control plane, a warm pool, deployed apps reachable from your browser) on your laptop. It diverges from production in deliberate ways (no Kata, plain HTTP, served source store, opt-in warm pools) covered below.

## One command

```bash
make dev
```

That target stands up a kind cluster, installs the testbed (local registry, Keycloak, agent-sandbox, observability stack), builds and loads the vibed/controller/router/static-nginx images, and helm-installs vibeD with the dev overlay. It takes ~5–10 minutes on a first run; subsequent runs are much faster thanks to layer caching.

When it finishes:

| URL | Purpose |
|---|---|
| `http://localhost:8080/` | vibeD dashboard + REST API + MCP server (`/mcp`) |
| `http://<id>.localhost/` | Deployed apps (port 80 implicit; `*.localhost` resolves to `127.0.0.1` in Chrome/Firefox) |
| `http://localhost:3000/` | Grafana (admin/admin): vibeD traces, logs, metrics |
| `http://localhost:9090/` | Prometheus |
| `http://localhost:31888/` | Keycloak (admin/admin) |

No `kubectl port-forward` is required; kind's `extraPortMappings` bridge host ports to the in-cluster Services.

## What the dev overlay changes vs production

`deploy/helm/vibed/values-kind.yaml` is the dev overlay. Read it before you deploy production with the same chart, because the differences matter:

- **`runtime.defaultClass: ""`.** No Kata RuntimeClass on kind; sandboxes are plain pods. Production uses `kata-qemu` (or `kata-fc`) for VM-level isolation.
- **`runtime.sandboxNetworkPolicy: Unmanaged` + `networkPolicy.enabled: false`.** Sandboxes have full DNS and cluster egress so the in-cluster blob store works. Production locks these down.
- **`config.storage.tarball.backend: served`.** vibeD serves source blobs from its own PVC over an in-cluster Service. Only works because dev sandboxes have cluster DNS. Production requires `s3` (pre-signed URLs).
- **`controller.urlScheme: http`, `urlPort: ""`.** Plain HTTP, port 80 implicit. Production uses `https` and Caddy's DNS-01 wildcard cert.
- **`:dev` images with `pullPolicy: Never`.** Built locally, loaded into the kind node via `kind load`. Production pulls from GHCR.
- **Caddy is `NodePort: 31080`.** Matches kind-config's `extraPortMappings` host:80 → container:31080. Production keeps it `ClusterIP` behind an external LB.
- **Only the `static-nginx` warm pool is enabled.** Static-site deploys work out of the box. Python/Node/Go/base require an opt-in (see below).

## Warm pools

`make dev` runs the three common app shapes, `static-nginx` (fast lane) plus
`node-24` and `python-313` (general lane), so static, JS and Python sources
deploy with no extra step. The general lane needs no Kata or nested virt here:
`runtime.defaultClass` is empty on Kind, so its pods schedule normally.

`go-123` and `base-al2023` stay opt-in; each runner image is its own ~30s
build, and they are rarely needed in dev. Deploy a Go source without its pool
and you'll see:

```
Phase=Failed
Reason=TemplateMissing
Message=no SandboxTemplate for template "go-123" (no warm pool configured for it)
```

That's by design: vibeD fails fast rather than retrying forever. Note that
this failure happens *before* a pod exists, so there are no logs: the reason
and message on the app are the whole diagnosis, and both `GET /v1/apps/{id}`
and the MCP tools return them. Enable the slot you need:

```bash
# Builds vibed-template-go-123:dev, loads it into kind, helm-upgrades to flip
# warmPools.go-123.enabled=true, and restarts the controller so the BYO
# validator re-checks now (otherwise the first deploy may fail on a stale
# validation cache).
make enable-go-pool

# The base kitchen-sink image works the same way:
make enable-base-pool
```

`make enable-node-pool` and `make enable-python-pool` still exist; use them to
switch a slot back on if you disabled it.

After ~30s the warm pod is `Ready`, the validation ConfigMap shows `valid:true`,
and `POST /v1/deploy` with a matching source goes `Phase=Ready` instead of
`Failed`.

## Verify

```bash
kubectl get pods -n vibed-system            # control plane: Running
kubectl get pods -n vibed-apps              # warm pool pods: Running, Ready
kubectl get sandboxwarmpools -n vibed-apps  # READY column matches replicas
curl http://localhost:8080/readyz           # all components ready
```

If any control-plane pod is not `Running`, see [troubleshooting](../deployment/troubleshooting.md).

## Teardown

```bash
bash testbed/kind-cluster/teardown.sh
# or `make teardown` if /usr/bin/make on macOS isn't tripping the
# Xcode license prompt
```

This deletes the kind cluster and everything in it: control plane, warm pools, deployed apps, observability. Bring it back with `make dev`.
