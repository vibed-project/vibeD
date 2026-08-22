---
title: Migrating to v0.6
sidebar_label: Migrating to v0.6
sidebar_position: 2
---

# Migrating to v0.6

v0.6 removes the legacy orchestrator and its REST/MCP surfaces. The live `/v1`
API is now the only deploy/lifecycle path. This page lists what was removed and
its `/v1` replacement so you can update clients, scripts, and integrations.

:::info Deploy path is unchanged
`POST /v1/deploy`, the sandbox runtime, lanes/templates, and the dashboard's
live event stream (`/api/events`) are unaffected. If you already use `/v1`, no
changes are required.
:::

## Removed: the `/api/artifacts*` REST surface

The original REST surface under `/api/artifacts*` is removed. Every operation
has a `/v1` equivalent:

| Legacy `/api/artifacts*`                        | `/v1` replacement                          |
| ----------------------------------------------- | ------------------------------------------ |
| `GET /api/artifacts` (list)                     | `GET /v1/apps`                             |
| `GET /api/artifacts/{id}` (get)                 | `GET /v1/apps/{id}`                        |
| `DELETE /api/artifacts/{id}` (delete)           | `DELETE /v1/apps/{id}`                     |
| `GET /api/artifacts/{id}/logs` (logs)           | `GET /v1/apps/{id}/logs`                   |
| `GET /api/artifacts/{id}/versions` (versions)   | `GET /v1/apps/{id}/versions`              |
| `POST /api/artifacts/{id}/rollback` (rollback)  | `POST /v1/apps/{id}/rollback`             |
| `POST /api/artifacts/{id}/share-link` (create)  | `POST /v1/apps/{id}/share-links`          |
| `GET /api/artifacts/{id}/share-links` (list)    | `GET /v1/apps/{id}/share-links`           |

See the [HTTP API Reference](./reference/http-api.md) for request/response
shapes. The `/v1` list and get endpoints are owner-scoped and return the
`App` shape (`app_id`, `phase`, `runtime`, `url`, …) rather than the legacy
artifact shape.

## Removed: deployment targets

`GET /api/targets` and the MCP tool `list_deployment_targets` are removed. The
deployment-target/backend model they exposed is superseded by
[lanes and templates](./concepts/lanes-and-templates.md): the classifier picks
a lane (`fast` / `general`) and a runtime template, and you can override either
in the deploy metadata (`runtime.lane`, `runtime.template`). There is no
separate target inventory to query.

## Removed: user-grant sharing

Sharing an app with specific user IDs is removed, along with its MCP tools
`share_artifact` and `unshare_artifact` and the `/api/artifacts/{id}/share`
and `/api/artifacts/{id}/unshare` endpoints.

Public **share links** are unaffected and remain the way to grant access. A
share link is a shareable, optionally password-protected URL that lets an
account-less viewer see an app's name, status, and URL. Share-link management
moved onto `/v1`:

| Action        | Endpoint                                    |
| ------------- | ------------------------------------------- |
| Create a link | `POST /v1/apps/{id}/share-links`            |
| List links    | `GET /v1/apps/{id}/share-links`             |
| Resolve a link (public) | `GET /api/share/{token}` (unchanged) |
| Revoke a link | `DELETE /api/share-links/{token}` (unchanged) |

The MCP tools `create_share_link`, `list_share_links`, and `revoke_share_link`
continue to work and now back onto these `/v1` endpoints.

## Changed: garbage collection

The garbage collector now keys off `VibedApp` custom resources. Live-path
resources are owner-referenced by their `VibedApp` and are cascade-deleted by
Kubernetes when the app is deleted, so the GC no longer drives their removal.
It is now a `SandboxClaim`-orphan backstop only: it reaps a `SandboxClaim`
whose owning `VibedApp` is gone and whose owner-ref cascade did not fire, once
the claim is older than `gc.maxAge`. `gc.enabled`, `gc.interval`, `gc.maxAge`,
and `gc.dryRun` are unchanged; see
[Configuration Reference](./configuration/config-reference.md#gc).

### One-time cleanup of pre-v0.6 debris

Because the orchestrator is gone, nothing stamps `vibed.dev/artifact-id` any
more, and the GC does **not** sweep resources that carry it. If you are
upgrading from `<= v0.5`, the legacy deployer's leftovers (build Jobs, deploy
ConfigMaps, Deployments, and `Sandbox` CRs) are not reclaimed automatically.
They are inert (nothing reconciles them), but they consume quota and clutter
the namespace, so clean them up once after upgrading:

```bash
NS=vibed-system   # your vibeD namespace

# Preview first.
kubectl -n "$NS" get jobs,deployments,sandboxes \
  -l 'app.kubernetes.io/managed-by=vibed,vibed.dev/artifact-id'

# ConfigMaps: exclude the artifact store, which is NOT legacy debris.
kubectl -n "$NS" get configmaps \
  -l 'app.kubernetes.io/managed-by=vibed,vibed.dev/artifact-id,app.kubernetes.io/component!=artifact-store'
```

Re-run each command with `delete` in place of `get` once the output looks
right. Do this only after every app has been redeployed through `/v1`.

:::caution Keep the artifact store
The `app.kubernetes.io/component!=artifact-store` selector matters: those
ConfigMaps hold artifact metadata, not deploy state. Deleting them loses app
history.
:::

## Note for policy authors

If you implement a [policy gate](./extending/policy-and-metering.md), prefer the
streaming `policy.Input.SourceOpener` accessor over reading the whole
`policy.Input.Source` byte slice for large payloads. `Source` is retained for
compatibility but reading it materializes the full source tarball in memory; the
streaming accessor lets a content policy scan the upload without buffering it.
