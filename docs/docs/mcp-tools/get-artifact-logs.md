---
sidebar_position: 7
---

# get_artifact_logs

Retrieve recent log lines from a deployed artifact's pods for debugging purposes.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `artifact_id` | string | Yes | ID of the artifact |
| `lines` | number | No | Number of log lines to return (default: 50) |

## Example

```json
{
  "artifact_id": "a1b2c3d4",
  "lines": 100
}
```

## Response

```json
{
  "logs": "2026-03-14T10:00:01Z Server started on :8080\n2026-03-14T10:00:02Z GET / 200 3ms\n..."
}
```

Logs are fetched from the running pod(s) using the Kubernetes API. For Knative services scaled to zero, the response will indicate no pods are available.
