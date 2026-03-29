---
sidebar_position: 3
---

# list_artifacts

List deployed artifacts with their status, deployment target, and access URLs. Supports pagination.

## Input Schema

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `status` | string | No | Filter by status: `running`, `building`, `failed`, `all` (default: `all`) |
| `offset` | int | No | Number of artifacts to skip (default: 0) |
| `limit` | int | No | Max artifacts to return (default: 50, max: 200) |

## Example

```json
{
  "status": "running",
  "offset": 0,
  "limit": 10
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
      "url": "http://my-portfolio.default.127.0.0.1.sslip.io",
      "created_at": "2026-03-14T10:00:00Z"
    }
  ],
  "total": 42,
  "offset": 0,
  "limit": 10
}
```

## Pagination

Use `offset` and `limit` to paginate through large artifact lists:

- **First page:** `{"limit": 10}` (offset defaults to 0)
- **Next page:** `{"offset": 10, "limit": 10}`
- **Check for more:** `offset + len(artifacts) < total`

The `total` field contains the count of all matching artifacts (before pagination), useful for displaying "showing 10 of 42".

## REST API

```
GET /api/artifacts?status=running&offset=0&limit=10
```

Returns the same JSON structure with `artifacts`, `total`, `offset`, and `limit` fields.
