---
sidebar_position: 2
---

# deploy_artifact

The primary tool for deploying web artifacts to the cluster.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | Yes | Unique DNS-safe name (lowercase, hyphens OK) |
| `files` | object | Yes | Map of relative file paths to file content |
| `language` | string | No | Language hint (nodejs, python, go, static) |
| `target` | string | No | Deployment target (auto, knative, kubernetes) |
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
  "url": "http://my-portfolio.default.127.0.0.1.sslip.io",
  "target": "knative",
  "status": "running",
  "image_ref": "vibed-artifacts/my-portfolio:latest"
}
```

## What Happens

1. **Validates** the name (DNS-safe, unique)
2. **Stores** source files to the configured storage backend
3. **Detects** the best deployment target
4. **Builds** a container image using Buildah (runs as a Kubernetes Job)
5. **Deploys** to the cluster (Knative Service, K8s Deployment, or OAM App)
6. **Returns** the access URL and artifact metadata
