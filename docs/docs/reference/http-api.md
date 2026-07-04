---
sidebar_position: 1
---

# HTTP API Reference

The `vibed` control plane serves an HTTP API under `/v1`, alongside the MCP endpoint, the dashboard, and the operational probes. This page is the operator/developer reference for those routes.

The API is defined by an OpenAPI spec committed at [`api/openapi.yaml`](https://github.com/vibed-project/vibeD/blob/main/api/openapi.yaml). The Go server stubs are generated from it by oapi-codegen (`make openapi-gen`); the spec is the source of truth for routing. A browsable **Swagger UI** is served at [`/api/docs/`](#swagger-ui), and the raw spec at `/api/docs/openapi.yaml`.

:::info Base URL
Everything below is relative to the server's base URL — `http://localhost:8080` in local dev, or `https://vibed.<your-domain>` in production. Set it via [`server.baseURL`](../configuration/config-reference.md) so generated share links are correct.
:::

## Authentication

All `/v1/*` routes require a **bearer token** unless auth is disabled (dev only). Pass it in the `Authorization` header:

```
Authorization: Bearer <token>
```

The token is either an OIDC-issued JWT or a configured API key, depending on `auth.mode`. See [Authentication & HTTPS](../configuration/authentication.md) for how tokens are issued and validated. When `auth.enabled` is `false`, every request is treated as an admin — convenient for a local Kind cluster, never acceptable when the API is exposed.

Requests without a valid token get `401` with an [error body](#errors). The operational probes (`/healthz`, `/readyz`, `/metrics`) are **public** — they carry no auth so a load balancer or Prometheus can scrape them.

Apps are **owner-scoped**: `GET /v1/apps` and the per-app routes only return apps whose `owner` matches the authenticated user. A caller asking for an app it doesn't own gets `404`, not `403` — vibeD deliberately does not confirm that another user's app exists. The governance audit trail ([`GET /v1/audit`](#get-v1audit)) is the one **admin-only** route.

## Operational endpoints

These carry no auth and are meant for probes, load balancers, and metrics scrapers.

| Method | Path       | Purpose                          | Success | Failure |
| ------ | ---------- | -------------------------------- | ------- | ------- |
| `GET`  | `/healthz` | Liveness probe                   | `200`   | `503`   |
| `GET`  | `/readyz`  | Readiness probe                  | `200`   | `503`   |
| `GET`  | `/metrics` | Prometheus exposition            | `200`   | —       |

`/readyz` returns `503` until Kubernetes clients, storage, the artifact store, and the HTTP server have all come up. Wire it as your readiness probe so traffic isn't routed before dependencies are live. `/metrics` returns the standard Prometheus text format; see [Monitoring](../deployment/monitoring.md).

```bash
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
curl -fsS http://localhost:8080/metrics | head
```

## Deploy & lifecycle

### `POST /v1/deploy`

Deploy a new app. This is a **multipart upload** with two parts:

| Part       | Type              | Required | Notes                                             |
| ---------- | ----------------- | -------- | ------------------------------------------------- |
| `source`   | file (binary)     | yes      | gzipped tarball of the source tree, **≤ 50 MB**   |
| `metadata` | JSON string field | yes      | at least `name`; see [metadata](#deploy-metadata) |

The classifier inspects the source file names to pick a lane and template; an explicit `runtime.lane` or `runtime.template` in `metadata` is an override. There is no container build on this path — the source is injected into a pre-booted warm sandbox.

```bash
curl -X POST http://localhost:8080/v1/deploy \
  -H "Authorization: Bearer $VIBED_TOKEN" \
  -F 'metadata={"name":"my-tool"};type=application/json' \
  -F 'source=@my-tool.tar.gz;type=application/gzip'
```

**Responses.** The endpoint is bound by a latency contract — it returns within ~10 s with one of:

| Status | Meaning                                                                        |
| ------ | ------------------------------------------------------------------------------ |
| `200`  | App is `Ready`; the warm pool absorbed the request. Body has a live `url`.     |
| `202`  | Accepted onto a slower path; app not yet `Ready`. Poll `status_url`.           |
| `400`  | Bad multipart, invalid metadata JSON, or missing `name`/`source`.              |
| `401`  | Missing or invalid bearer token.                                               |
| `403`  | A deploy-time policy gate denied the request.                                  |
| `413`  | Tarball exceeds the 50 MB limit.                                               |
| `429`  | A per-owner or per-department quota was exceeded.                              |
| `500`  | Internal error.                                                                |

The `200`/`202` body is a `DeployResponse`:

```json
{
  "app_id": "my-tool",
  "url": "https://my-tool.vibed.example.com",
  "status_url": "/v1/apps/my-tool"
}
```

`app_id` is always present. On `200`, `url` is set and the app is live. On `202`, `status_url` points at [`GET /v1/apps/{id}`](#get-v1appsid) — poll it until `phase` reaches `Ready` or `Failed`.

#### Deploy metadata

The `metadata` field is a JSON object:

```json
{
  "name": "my-tool",
  "runtime": { "lane": "general", "template": "node-24", "entrypoint": "server.js" },
  "egress":  { "allowed_hosts": ["api.example.com", "*.internal.example.com"] },
  "env":     [{ "name": "LOG_LEVEL", "value": "info" }],
  "ttl":     "30m"
}
```

| Field      | Required | Description                                                                                     |
| ---------- | -------- | ----------------------------------------------------------------------------------------------- |
| `name`     | yes      | URL-safe slug (`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`, ≤ 63 chars). Becomes the `app_id` and subdomain. |
| `runtime`  | no       | Classifier override. `lane` (`fast` \| `general`), `template`, and `entrypoint` are each optional. |
| `egress`   | no       | Per-app outbound allow-list. Omitting it (or an empty list) means no external egress. See [Egress Control](../configuration/egress-control.md). |
| `env`      | no       | Environment variables injected into the runner.                                                 |
| `ttl`      | no       | Idle TTL before suspend (Go duration, default `30m`).                                            |

### `GET /v1/apps`

List apps owned by the authenticated user.

```bash
curl -H "Authorization: Bearer $VIBED_TOKEN" http://localhost:8080/v1/apps
```

```json
{
  "items": [
    {
      "app_id": "my-tool",
      "name": "my-tool",
      "owner": "alice@example.com",
      "phase": "Ready",
      "url": "https://my-tool.vibed.example.com",
      "runtime": { "lane": "general", "template": "node-24" },
      "last_deployed_at": "2026-07-03T10:12:00Z"
    }
  ]
}
```

`phase` is one of `Pending`, `Claiming`, `Starting`, `Ready`, `Suspended`, `Failed` (see [App Lifecycle](../concepts/app-lifecycle.md)). Returns `401` without a token.

### `GET /v1/apps/{id}`

Get a single app's status and URL. The path parameter matches `^[a-z0-9-]{1,63}$`.

```bash
curl -H "Authorization: Bearer $VIBED_TOKEN" http://localhost:8080/v1/apps/my-tool
```

Returns the same `App` shape as a list item. Responses: `200` (found), `401` (unauthenticated), `404` (not found or not owned by the caller). This is the endpoint a client polls after a `202` from deploy.

### `DELETE /v1/apps/{id}`

Tear down an app. Deletes the `VibedApp` (the controller reaps the bound sandbox pod) and best-effort removes the stored source tarball.

```bash
curl -X DELETE -H "Authorization: Bearer $VIBED_TOKEN" http://localhost:8080/v1/apps/my-tool
```

Responses: `204` (deleted, empty body), `401`, `404`.

### `GET /v1/apps/{id}/logs`

Stream logs from the app's runner as a **Server-Sent Events** (SSE) stream. Ownership is checked up front, so a `404` is returned cleanly before the connection is upgraded.

```bash
curl -N -H "Authorization: Bearer $VIBED_TOKEN" \
  http://localhost:8080/v1/apps/my-tool/logs
```

The response is `text/event-stream`; each log line arrives as one event:

```
data: 2026-07-03T10:12:01Z starting server on :8080

data: 2026-07-03T10:12:02Z listening
```

If the stream fails mid-flight, a terminal `event: error` frame is emitted. A client disconnect (cancelled request) is not an error. Concurrent streams per user are capped by [`limits.maxConcurrentLogStreamsPerUser`](../configuration/config-reference.md); exceeding it returns `429` with a `Retry-After` header. Responses: `200` (stream), `401`, `404`, `429`.

## Governance

### `GET /v1/audit`

The **admin-only** governance audit trail: an append-only record of mutating actions (deploy, delete, suspend, resume), newest first. Non-admin callers get `403`. This route is not part of the generated OpenAPI surface; it is mounted directly and gated on the admin role.

Query parameters (all optional) filter the result:

| Param    | Filters on                                       |
| -------- | ------------------------------------------------ |
| `actor`  | authenticated user ID that performed the action  |
| `action` | `deploy` \| `delete` \| `suspend` \| `resume`    |
| `app`    | target app / artifact name                       |
| `limit`  | max number of events to return                   |

```bash
curl -H "Authorization: Bearer $VIBED_TOKEN" \
  "http://localhost:8080/v1/audit?action=deploy&limit=50"
```

```json
{
  "events": [
    {
      "id": "01J...",
      "time": "2026-07-03T10:12:00Z",
      "actor": "alice@example.com",
      "action": "deploy",
      "target": "my-tool",
      "outcome": "ok",
      "tenant_id": "default",
      "source_hash": "sha256:..."
    }
  ]
}
```

Whether events persist depends on the store backend — see [Audit Trail](../configuration/audit-log.md). With the SQLite backend the trail is durable; with the memory/configmap backends it is in-memory and does not survive a restart.

## Internal source blobs

### `GET /internal/sources/{id}.tar.gz`

The source blob that the in-sandbox agent (`vibed-agent`) pulls on startup. This route is served **only** when the tarball store uses the `served` backend (dev). Requests must be `GET`/`HEAD` for a path ending in `.tar.gz`; anything else is `404` or `405`. There are no directory listings.

It sits behind the same auth middleware as `/v1`, so only the shared agent token can pull. In production the `served` backend is not used: sandboxes have no cluster DNS or cluster-internal egress under the restrictive NetworkPolicy, so the agent instead pulls from a pre-signed **S3** URL and vibeD serves nothing here. See [Storage](../configuration/storage.md) for `served` vs `s3`.

## MCP endpoint

The Model Context Protocol server is exposed over streamable HTTP at **`/mcp`** (and `/mcp/`). MCP clients (Claude Desktop, Cursor, Goose, …) connect there to call vibeD's tools; the same bearer-token auth applies. The MCP tools cover the same deploy/list/status/logs surface as this API — see the [MCP Tools](../mcp-tools/overview.md) reference.

## Swagger UI

A self-contained Swagger UI is served at **`/api/docs/`** (a bare `/api/docs` redirects to it), reading the spec from `/api/docs/openapi.yaml`. Use it to browse schemas and try requests interactively against a running instance.

## Errors

Error responses share a stable JSON shape:

```json
{
  "code": "deploy_failed",
  "message": "human-readable description",
  "details": {}
}
```

`code` is a stable, machine-readable string (e.g. `unauthenticated`, `missing_name`, `not_found`, `policy_denied`, `quota_exceeded`, `too_many_log_streams`); `message` is human-readable; `details` is optional structured context.
