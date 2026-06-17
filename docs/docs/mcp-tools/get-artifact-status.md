---
sidebar_position: 4
---

# get_artifact_status

Get detailed status information for a specific deployed artifact, including URL, deployment target, image reference, and any errors.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to check |

## Example

```json
{
  "artifact_id": "a1b2c3d4"
}
```

## Response

Returns the full artifact object:

```json
{
  "id": "a1b2c3d4",
  "name": "my-portfolio",
  "status": "running",
  "target": "sandbox",
  "mode": "built",
  "url": "http://my-portfolio.default.localhost:31080",
  "image_ref": "nginx:alpine",
  "language": "static",
  "port": 80,
  "version": 3,
  "owner_id": "env-key",
  "created_at": "2026-03-14T10:00:00Z",
  "updated_at": "2026-03-14T12:30:00Z"
}
```

The status reflects the app's [phase](../concepts/app-lifecycle.md) (`Pending` → `Claiming` → `Starting` → `Ready`, or `Failed`) along with its URL and the bound sandbox.

Note: environment variables and secret references are never included in the response — secret values are never exposed.
