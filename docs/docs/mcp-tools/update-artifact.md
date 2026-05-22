---
sidebar_position: 5
---

# update_artifact

Update an existing deployed artifact with new source files. Triggers a rebuild and redeployment. A new version snapshot is created automatically.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to update |
| `files` | object | Yes | Updated file map (full replacement of source files) |
| `env_vars` | object | No | Updated environment variables |
| `secret_refs` | object | No | Updated secret references in format `secret-name:key` |

## Example

```json
{
  "artifact_id": "a1b2c3d4",
  "files": {
    "index.html": "<!DOCTYPE html><html><body>Updated!</body></html>",
    "style.css": "body { background: #000; color: #fff; }"
  },
  "env_vars": {
    "NODE_ENV": "production"
  }
}
```

## Response

```json
{
  "artifact_id": "a1b2c3d4",
  "name": "my-portfolio",
  "url": "http://my-portfolio.default.localhost:31080",
  "target": "knative",
  "status": "running",
  "image_ref": "kind-registry:5000/vibed-artifacts/my-portfolio:v2",
  "version": 2
}
```

## What Happens

1. **Validates** the app exists and the caller owns it
2. **Stores** the new source tarball (replaces the previous source)
3. **Re-injects** the new source into the app's sandbox — no rebuild

:::note
Redeploy (`POST /v1/apps/{id}/redeploy`) is not yet fully wired in v0.3.x. The supported update path is to deploy again under the same name.
:::
