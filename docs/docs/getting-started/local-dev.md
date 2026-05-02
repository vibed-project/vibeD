---
sidebar_position: 2
---

# Local Development Setup

vibeD uses **Podman + Kind** for local development. This guide sets up a complete environment.

## One-Command Setup

```bash
make dev
```

This creates a Kind cluster, installs Knative Serving with Kourier, and builds vibeD.

## Manual Setup

### 1. Create Kind Cluster

```bash
make setup-cluster
```

This creates a Kind cluster named `vibed-dev` with port mappings:
- Port 80 → Kourier ingress (for Knative services)
- Port 443 → Kourier ingress (HTTPS)

### 2. Install Knative Serving

```bash
make install-knative
```

This installs:
- Knative Serving CRDs and core components
- Kourier as the ingress layer (NodePort mode)
- Localhost domain for automatic URL resolution

### 3. Build and Run vibeD

```bash
# Build
make build

# Run in HTTP mode (dashboard + MCP endpoint)
make run-http
```

### 4. Access the Dashboard

Open `http://localhost:8080` in your browser. You should see the vibeD dashboard with deployment target status.

## Accessing Deployed Artifacts

With the `.localhost` domain suffix configured, artifacts deployed via Knative get URLs like:

```
http://my-app.default.localhost
```

Modern operating systems (like macOS, Linux, and Windows 11) automatically resolve any domain ending in `.localhost` to `127.0.0.1`, which then routes through the ingress layer on port 80.

### Port-Forward Access

If your Kind cluster doesn't expose port 80 to the host (common with Podman), you need to port-forward the ingress service. The command depends on which networking layer you installed:

**Kourier:**

```bash
kubectl port-forward svc/kourier 8081:80 -n kourier-system
```

**Contour (Envoy):**

```bash
kubectl port-forward svc/envoy 8081:80 -n projectcontour
```

Then access artifacts by appending the port to the URL:

```
http://my-app.default.localhost:8081
```

:::tip Using port 80 directly
If you want to use the default port (so URLs work without `:8081`), use `sudo` to bind to port 80:

```bash
sudo kubectl port-forward svc/envoy 80:80 -n projectcontour
```
:::

### Accessing the vibeD MCP Endpoint

To connect AI tools (like Claude Desktop) to the vibeD MCP server running in-cluster, port-forward the vibeD service:

```bash
kubectl port-forward svc/vibed 9090:8080 -n vibe-system
```

The MCP endpoint is then available at `http://localhost:9090/mcp/`. If vibeD is exposed via an HTTPProxy/Ingress (e.g. `vibed.localhost`), you can also reach it through the ingress port-forward on port 8081.

### DNS Troubleshooting

If your browser cannot resolve `.localhost` domains (older Windows versions or custom network setups), you can add entries manually to your `/etc/hosts` file (or `C:\Windows\System32\drivers\etc\hosts` on Windows):

```bash
sudo sh -c 'echo "127.0.0.1 my-app.default.localhost" >> /etc/hosts'
```

## Teardown

```bash
make teardown
```
