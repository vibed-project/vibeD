---
sidebar_position: 11
---

# share_artifact

Share a deployed artifact with other users, granting them read-only access to view status, logs, and the access URL. Only the artifact owner or an admin can share.

Requires [authentication](/docs/configuration/authentication) to be enabled.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to share |
| `user_ids` | string[] | Yes | List of user IDs to grant read-only access to |

## Example

```json
{
  "artifact_id": "a1b2c3d4",
  "user_ids": ["alice", "bob"]
}
```

## Response

```json
{
  "artifact_id": "a1b2c3d4",
  "message": "shared with 2 users"
}
```

Shared users can call `get_artifact_status`, `get_artifact_logs`, and `list_versions` but cannot update, delete, or share the artifact.
