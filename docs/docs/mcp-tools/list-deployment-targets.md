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
    }
  ]
}
```

This tool lists the available runtime [templates](../concepts/lanes-and-templates.md) (e.g. `node-24`, `python-313`, `static-nginx`). You normally don't pass a template — the classifier picks one — but you can override it in the deploy metadata.
