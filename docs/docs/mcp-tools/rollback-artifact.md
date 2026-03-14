---
sidebar_position: 10
---

# rollback_artifact

Roll back a deployed artifact to a previous version. This redeploys the artifact using the image and configuration from the specified version snapshot. A new version entry is created for the rollback — history is never rewritten.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to roll back |
| `version` | number | Yes | Target version number to roll back to |

## Example

```json
{
  "artifact_id": "a1b2c3d4",
  "version": 1
}
```

## Response

```json
{
  "artifact_id": "a1b2c3d4",
  "name": "my-portfolio",
  "url": "http://my-portfolio.default.127.0.0.1.sslip.io:31080",
  "target": "knative",
  "status": "running",
  "image_ref": "kind-registry:5000/vibed-artifacts/my-portfolio:v1",
  "new_version": 3,
  "message": "Rolled back to version 1 (now version 3)"
}
```

## What Happens

1. **Fetches** the target version snapshot
2. **Restores** the image reference, environment variables, and secret refs from that version
3. **Redeploys** the artifact with the restored configuration
4. **Creates** a new version entry (e.g., version 3) pointing to the old image
5. Use `list_versions` to see the full version history including rollbacks
