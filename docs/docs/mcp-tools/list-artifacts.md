---
sidebar_position: 3
---

# list_artifacts

List all deployed artifacts with their status, deployment target, and access URLs.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `status` | string | No | Filter by status: `running`, `building`, `failed`, `all` (default: `all`) |

## Example

```json
{
  "status": "running"
}
```

## Response

```json
{
  "artifacts": [
    {
      "id": "a1b2c3d4",
      "name": "my-portfolio",
      "status": "running",
      "target": "knative",
      "url": "http://my-portfolio.default.127.0.0.1.sslip.io:31080",
      "created_at": "2026-03-14T10:00:00Z"
    }
  ]
}
```
