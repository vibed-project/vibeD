---
sidebar_position: 13
---

# create_share_link

Create a public shareable link for an artifact. Anyone with the link (and optional password) can view the artifact's name, status, and URL without a vibeD account.

Requires [authentication](/docs/configuration/authentication) and the SQLite store backend.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to create a share link for |
| `password` | string | No | Optional password to protect the link |
| `expires_in` | string | No | Expiration duration (e.g. `24h`, `7d`). Empty means no expiration. |

## Example

```json
{
  "artifact_id": "a1b2c3d4",
  "password": "s3cret",
  "expires_in": "7d"
}
```

## Response

```json
{
  "token": "a8f3...64-char-hex-token",
  "artifact_id": "a1b2c3d4",
  "created_by": "alice",
  "has_password": true,
  "expires_at": "2026-03-21T10:00:00Z",
  "created_at": "2026-03-14T10:00:00Z",
  "revoked": false
}
```

The share link URL is `GET /api/share/{token}`. Password-protected links return 401 until a correct password is POSTed.

## REST API

```
POST /api/artifacts/{id}/share-link
Body: {"password": "...", "expires_in": "24h"}
```

## Security

- Tokens are 256-bit cryptographically random (not guessable)
- Passwords are bcrypt-hashed (vibeD never stores plaintext)
- Expired and revoked links return 404 (no information leakage)
- Only the artifact owner or admin can create share links
