---
sidebar_position: 12
---

# unshare_artifact

Revoke read-only access to a deployed artifact from specific users. Only the artifact owner or an admin can unshare.

Requires [authentication](/docs/configuration/authentication) to be enabled.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to unshare |
| `user_ids` | string[] | Yes | List of user IDs to revoke access from |

## Example

```json
{
  "artifact_id": "a1b2c3d4",
  "user_ids": ["bob"]
}
```

## Response

```json
{
  "artifact_id": "a1b2c3d4",
  "message": "unshared from 1 users"
}
```
