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
| `target` | string | No | Deployment target (auto, sandbox, kubernetes) |
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

1. **Validates** the name (DNS-safe, unique).
2. **Stores** the source tarball to the configured [source store](../configuration/storage.md).
3. **Classifies** the source into a [lane and template](../concepts/lanes-and-templates.md).
4. **Creates** a [`VibedApp`](../concepts/app-lifecycle.md) and the controller **claims a warm sandbox** and injects the source — no container build on the deploy path.
5. **Returns** the access URL once the app reaches `Ready` (or a `status_url` to poll if it took a slow path).

See [App lifecycle](../concepts/app-lifecycle.md) for the phases the app moves through.
