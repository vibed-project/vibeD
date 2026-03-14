---
sidebar_position: 9
---

# list_versions

List all version snapshots for a deployed artifact, ordered by version number. Each version captures the image reference, status, URL, and configuration at that point in time.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to list versions for |

## Example

```json
{
  "artifact_id": "a1b2c3d4"
}
```

## Response

```json
{
  "artifact_id": "a1b2c3d4",
  "versions": [
    {
      "version": 1,
      "status": "running",
      "url": "http://my-portfolio.default.127.0.0.1.sslip.io:31080",
      "image_ref": "kind-registry:5000/vibed-artifacts/my-portfolio:v1",
      "created_at": "2026-03-14T10:00:00Z"
    },
    {
      "version": 2,
      "status": "running",
      "url": "http://my-portfolio.default.127.0.0.1.sslip.io:31080",
      "image_ref": "kind-registry:5000/vibed-artifacts/my-portfolio:v2",
      "created_at": "2026-03-14T12:30:00Z"
    }
  ]
}
```

A new version is created on each `update_artifact` or `rollback_artifact` call. Versions are immutable snapshots — they are never modified after creation.
