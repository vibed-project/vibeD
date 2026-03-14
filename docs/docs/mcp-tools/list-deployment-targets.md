---
sidebar_position: 8
---

# list_deployment_targets

Show which deployment backends are available in the current cluster. vibeD auto-detects targets by checking for CRDs and API groups.

## Input Schema

No parameters required.

## Example

```json
{}
```

## Response

```json
{
  "targets": [
    {
      "name": "knative",
      "available": true
    },
    {
      "name": "kubernetes",
      "available": true
    },
    {
      "name": "wasmcloud",
      "available": false
    }
  ]
}
```

This tool is useful for AI agents to decide which `target` to pass to `deploy_artifact`. When `target` is set to `auto` (the default), vibeD uses this detection internally to pick the best target.
