---
sidebar_position: 3
---

# Artifact Lifecycle

Every artifact goes through a well-defined lifecycle from creation to deletion.

## States

| State | Description |
|-------|-------------|
| `pending` | Artifact record created, waiting for processing |
| `building` | Source code stored, container image being built |
| `deploying` | Image built, deploying to cluster |
| `running` | Successfully deployed, accessible via URL |
| `failed` | Build or deployment failed (check error field) |
| `deleted` | Removed from cluster and store |

## Flow

```
deploy_artifact called
        │
        ▼
    ┌─────────┐
    │ pending  │
    └────┬────┘
         │ Store source files
         ▼
    ┌──────────┐
    │ building │  ← Buildah Job creates container image
    └────┬─────┘
         │ Image ready
         ▼
    ┌───────────┐
    │ deploying │  ← Apply manifest to cluster
    └─────┬─────┘
          │ Deployment successful
          ▼
    ┌─────────┐
    │ running │  ← URL available, serving traffic
    └─────────┘
```

If any step fails, the artifact transitions to `failed` with an error message explaining what went wrong.

## Real-Time Events

Every state transition emits a Server-Sent Event through the EventBus. Connected dashboard clients (and any SSE subscriber at `GET /api/events`) receive these transitions instantly, without polling.

| Event Type | Trigger |
|------------|---------|
| `artifact.status_changed` | Any status transition (pending → building → deploying → running, or → failed) |
| `artifact.deleted` | Artifact removed from the cluster and store |

Each event payload includes the `artifact_id`, new `status`, optional `error`, and a `timestamp`. The dashboard uses these events to update the artifact list in real time, fetching full artifact details on status changes and removing entries on deletion.

## Ownership

When [authentication](../configuration/authentication.md) is enabled, each artifact is stamped with the deploying user's identity (`owner_id`). This ensures:

- Users only see their own artifacts when listing
- Update, delete, status, and log operations verify ownership
- Per-user storage routing directs artifacts to the user's configured repository

When auth is disabled, `owner_id` is empty and all artifacts are accessible to everyone.

## Update Flow

Calling `update_artifact` on a running artifact:
1. Verifies the caller owns the artifact (when auth enabled)
2. Stores new source files (overwrites previous)
3. Rebuilds the container image
4. Updates the deployment (new revision for Knative)
5. Returns the new URL

## Delete Flow

Calling `delete_artifact`:
1. Verifies the caller owns the artifact (when auth enabled)
2. Removes the deployment from the cluster
3. Deletes stored source code and manifests
4. Removes the artifact record from the store
