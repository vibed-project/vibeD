---
sidebar_position: 3
---

# First Deployment

Deploy your first artifact using vibeD's MCP tools.

## Using Claude Desktop

### Option A: HTTP Transport (Remote / In-Cluster)

If vibeD runs as a service (e.g. deployed to your Kind cluster), use [`mcp-remote`](https://www.npmjs.com/package/mcp-remote) to bridge Claude Desktop's stdio to vibeD's HTTP endpoint.

Add this to your Claude Desktop configuration (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "vibed": {
      "command": "npx",
      "args": [
        "mcp-remote",
        "http://vibed.localhost:9090/mcp/",
        "--allow-http"
      ]
    }
  }
}
```

:::info Prerequisites
- **Node.js 20+** must be installed (for `npx`)
- The vibeD MCP endpoint must be reachable from your machine — see [Port-Forward Access](./local-dev#port-forward-access) if running on a local Kind cluster
:::

### Option B: Stdio Transport (Direct)

Run vibeD as a local process that Claude Desktop launches directly:

```json
{
  "mcpServers": {
    "vibed": {
      "command": "/path/to/vibed",
      "args": ["--config", "/path/to/vibed.yaml"]
    }
  }
}
```

This starts vibeD in stdio mode. The binary needs access to a Kubernetes cluster via your local kubeconfig.

### Deploy Your First Artifact

Ask Claude to deploy a simple website:

> "Create a simple portfolio website with my name and deploy it using vibeD"

Claude will use the `deploy_artifact` tool automatically. How the deploy runs depends on the source:

- **Static HTML/CSS/JS** — deploys instantly via ConfigMap + nginx, no container build.
- **Python/Node with pre-baked dependencies** — if the [Instant Preview](../concepts/instant-preview.md) fast path is enabled, the source is injected into a warm pooled runner pod, also with no build. The artifact comes back with `mode: preview`; promote it with `promote_artifact` for a durable build.
- **Anything else** — built into a container image by a Buildah Kubernetes Job.

## Try an Instant Preview

If the fast path is enabled (`fastPath.enabled` in the vibeD config), deploy a tiny Python app and watch it come up without a build:

```json
{
  "name": "hello-flask",
  "files": {
    "app.py": "import os\nfrom flask import Flask\napp = Flask(__name__)\n\n@app.route('/')\ndef home():\n    return 'Hello from an Instant Preview!'\n\napp.run(host='0.0.0.0', port=int(os.environ['PORT']))\n"
  }
}
```

`flask` is pre-baked into the runner image, so this skips the build entirely and
returns `mode: preview`. Call `promote_artifact` with the returned `artifact_id`
when you want a durable, digest-pinned build.

## Using MCP Inspector

For testing, use the [MCP Inspector](https://github.com/modelcontextprotocol/inspector):

```bash
npx @modelcontextprotocol/inspector ./bin/vibed --config vibed.yaml
```

Then call the `deploy_artifact` tool with:

```json
{
  "name": "hello-world",
  "files": {
    "index.html": "<!DOCTYPE html><html><body><h1>Hello from vibeD!</h1></body></html>"
  }
}
```

## Using the HTTP API

If vibeD is running in HTTP mode, you can call the MCP endpoint directly via Streamable HTTP:

```bash
# 1. Initialize session
curl -X POST http://localhost:8080/mcp/ \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
    "protocolVersion":"2025-03-26","capabilities":{},
    "clientInfo":{"name":"curl","version":"1.0"}}}'

# 2. Use the Mcp-Session-Id header from the response for subsequent calls
```

## Check the Dashboard

After deploying, open `http://localhost:8080` to see your artifact in the dashboard with its status and access URL.
