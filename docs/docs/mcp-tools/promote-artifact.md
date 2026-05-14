---
sidebar_position: 6
---

# promote_artifact

Promote a fast-path [Instant Preview](../concepts/instant-preview.md) into a
durable built artifact. Runs the real container build, deploys the
digest-pinned image to a production backend, swaps the live deployment, and
recycles the pooled runner.

Only an artifact with `mode: preview` (running on a pooled runner) can be
promoted.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the fast-path preview artifact to promote |

## Example

```json
{
  "artifact_id": "a1b2c3d4"
}
```

## Response

Returns immediately with status `building` — poll
[`get_artifact_status`](./get-artifact-status.md) until status is `running`.

```json
{
  "artifact_id": "a1b2c3d4",
  "name": "my-api",
  "status": "building"
}
```

## What Happens

1. **Validates** the artifact is a running preview and the caller has permission
2. **Builds** the container image from the artifact's already-stored source
3. **Selects** a durable backend (Knative / Kubernetes / Sandbox)
4. **Deploys** the digest-pinned image and confirms it is up
5. **Releases** the pooled runner back to the pool (it is recycled)
6. **Flips** the artifact to `mode: built`

The `artifact_id` is stable across the promote. The **URL changes** — it moves
from the runner's address to the durable backend's URL, so re-fetch it with
`get_artifact_status`.

On **any failure**, the preview is restored and left running — a failed promote
never takes the preview down.

:::tip Auto-promote
Set `fastPath.autoPromote: true` in the vibeD config to promote every preview
automatically in the background — no explicit `promote_artifact` call needed.
:::
