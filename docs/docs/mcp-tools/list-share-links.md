---
sidebar_position: 14
---

# list_share_links

List all share links for an artifact. Only the artifact owner or an admin can see share links.

Requires [authentication](/docs/configuration/authentication) and the SQLite store backend.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to list share links for |

## Example

```json
{
  "artifact_id": "a1b2c3d4"
}
```

## Response

```json
[
  {
    "token": "a8f3...64-char-hex-token",
    "artifact_id": "a1b2c3d4",
    "created_by": "alice",
    "has_password": true,
    "expires_at": "2026-03-21T10:00:00Z",
    "created_at": "2026-03-14T10:00:00Z",
    "revoked": false
  }
]
```

## REST API

```
GET /api/artifacts/{id}/share-links
```
