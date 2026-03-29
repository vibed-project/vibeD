---
sidebar_position: 15
---

# revoke_share_link

Revoke a share link so it can no longer be used. The link will return 404 after revocation. Only the artifact owner or an admin can revoke share links.

Requires [authentication](/docs/configuration/authentication) and the SQLite store backend.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `token` | string | Yes | The share link token to revoke |

## Example

```json
{
  "token": "a8f3...64-char-hex-token"
}
```

## Response

```json
{
  "status": "revoked"
}
```

## REST API

```
DELETE /api/share-links/{token}
```
