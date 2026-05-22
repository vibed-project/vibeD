---
sidebar_position: 2
---

# Local Development Setup

vibeD develops on **Podman + Kind**. The dev install runs the full stack — control plane, warm pools, and sandboxes — on your laptop, with a few simplifications over production (see the dev overlay note below).

## Cluster + dependencies

You need a Kind cluster with [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) installed and a `kata-qemu` RuntimeClass (`kata-qemu` works without nested virtualization, which is why dev uses it). Build the component images and load them into the Kind node:

```bash
# Build the images (server, controller, router) and the template images,
# then load them into the Kind node so the chart's pullPolicy: Never works.
make build-images load-images   # see the Makefile for exact targets
```

## Install with the dev overlay

```bash
helm install vibed deploy/helm/vibed/ \
  -n vibed-system --create-namespace \
  -f deploy/helm/vibed/values-kind.yaml
```

`values-kind.yaml` is the dev overlay. It differs from production in ways you should understand:

- `runtime.sandboxNetworkPolicy: Unmanaged` and `networkPolicy.enabled: false` — sandboxes are fully open, so they keep normal **cluster DNS**.
- `config.storage.tarball.backend: served` — vibeD serves source blobs from its own PVC over the in-cluster Service DNS (works only because dev sandboxes have cluster DNS; production requires `s3`).
- `controller.urlScheme: http` + `urlPort: "18080"` — Caddy serves plain HTTP and is reached via a port-forward, so app URLs are `http://<label>.localhost:18080`.
- `:dev` images with `pullPolicy: Never` (loaded into the Kind node, not pulled).

## Access

The control plane (API, dashboard, MCP) and Caddy are ClusterIP services. Port-forward them:

```bash
kubectl port-forward -n vibed-system svc/vibed 18090:8080        # API + dashboard + MCP
kubectl port-forward -n vibed-system svc/vibed-caddy 18080:80     # app traffic
```

- **Dashboard / API:** `http://localhost:18090/`
- **MCP endpoint:** `http://localhost:18090/mcp` (see [connecting Claude Desktop](first-deployment.md))
- **Deployed apps:** `http://<label>.localhost:18080` — the URL vibeD returns. `*.localhost` resolves to `127.0.0.1` in Chrome/Firefox; in Safari add a `/etc/hosts` entry for the specific label, or use Chrome.

:::note Why a port and not `https://…localhost`
In production, Caddy terminates wildcard DNS-01 TLS and apps are `https://<label>.<domain>`. In dev there's no TLS and Caddy is behind a port-forward, so the controller is configured (via `urlScheme`/`urlPort`) to emit `http://<label>.localhost:18080`.
:::

## Teardown

```bash
helm uninstall vibed -n vibed-system
```

This removes the control plane and the `vibed-apps` namespace (warm pools, claims, and deployed apps go with it). The `VibedApp` CRD and agent-sandbox are left in place.
