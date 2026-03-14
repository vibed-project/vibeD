---
sidebar_position: 6
---

# delete_artifact

Stop and remove a deployed artifact. This deletes the deployment, stored source code, and all associated resources.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact to delete |

## Example

```json
{
  "artifact_id": "a1b2c3d4"
}
```

## Response

```json
{
  "message": "artifact my-portfolio deleted"
}
```

## What Happens

1. **Deletes** the Knative Service, Kubernetes Deployment, or wasmCloud component
2. **Removes** stored source files from the storage backend
3. **Removes** the artifact record from the store
4. **Emits** a `deleted` event via the EventBus
