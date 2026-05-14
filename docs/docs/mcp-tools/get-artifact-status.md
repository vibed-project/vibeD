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
  "target": "knative",
  "mode": "built",
  "url": "http://my-portfolio.default.localhost:31080",
  "image_ref": "kind-registry:5000/vibed-artifacts/my-portfolio:v1",
  "language": "static",
  "port": 80,
  "env_vars": {"NODE_ENV": "production"},
  "secret_refs": {"DB_PASSWORD": "my-db-creds:password"},
  "version": 3,
  "owner": "env-key",
  "created_at": "2026-03-14T10:00:00Z",
  "updated_at": "2026-03-14T12:30:00Z"
}
```

The `mode` field is `built` for a normal artifact or `preview` for an
ephemeral [Instant Preview](../concepts/instant-preview.md) running on a
pooled runner.

Note: `secret_refs` shows only the reference (`secret-name:key`), never the actual secret value.
