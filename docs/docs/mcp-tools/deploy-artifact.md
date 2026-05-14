---
sidebar_position: 2
---

# deploy_artifact

The primary tool for deploying web artifacts to the cluster.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | Yes | Unique DNS-safe name (lowercase, hyphens OK) |
| `files` | object | Yes | Map of relative file paths to file content. **Tip:** Provide a `Dockerfile` at the root to completely customize the build. |
| `language` | string | No | Language hint (nodejs, python, go, static) |
| `target` | string | No | Deployment target (auto, knative, sandbox, kubernetes, runner) |
| `env_vars` | object | No | Environment variables for the artifact |
| `secret_refs` | object | No | Map of env var name to K8s Secret reference (`secret-name:key`) |
| `port` | number | No | Port the app listens on (auto-detected) |

## Example

```json
{
  "name": "my-portfolio",
  "files": {
    "index.html": "<!DOCTYPE html><html>...</html>",
    "style.css": "body { font-family: sans-serif; }",
    "app.js": "console.log('Hello from vibeD!');"
  },
  "target": "auto"
}
```

## Response

```json
{
  "artifact_id": "a1b2c3d4e5f6g7h8",
  "name": "my-portfolio",
  "url": "http://my-portfolio.default.localhost",
  "target": "knative",
  "status": "running",
  "image_ref": "vibed-artifacts/my-portfolio:latest"
}
```

## What Happens

1. **Validates** the name (DNS-safe, unique)
2. **Stores** source files to the configured storage backend
3. **Detects** the best deployment target
4. **Fast path** — if the [Instant Preview](../concepts/instant-preview.md) fast
   path is enabled and the app is eligible (Python/Node, all dependencies
   pre-baked), vibeD **skips the build**: it claims a warm runner pod and
   injects the source. The artifact comes back with `mode: preview`.
5. **Builds** a container image using Buildah otherwise. If a `Dockerfile` is
   provided in the files map, it is used directly; otherwise one is
   auto-generated. (Static HTML/CSS/JS also skips the build, via ConfigMap +
   nginx.)
6. **Deploys** to the cluster (Knative Service, Sandbox, K8s Deployment, or a
   pooled runner)
7. **Returns** the access URL and artifact metadata

The response includes a `mode` field — `preview` for a fast-path deploy
(promote it with [`promote_artifact`](./promote-artifact) for a durable build),
`built` otherwise.
