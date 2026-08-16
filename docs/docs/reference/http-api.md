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

List apps owned by the authenticated user. Optional query parameters page the result:

| Param    | Type | Default | Description                                                          |
| -------- | ---- | ------- | -------------------------------------------------------------------- |
| `limit`  | int  | —       | Max number of apps to return. Omitted (or `0`) returns **all** apps. |
| `offset` | int  | `0`     | Number of apps to skip before collecting the page.                   |

```bash
# all apps (default — no params)
curl -H "Authorization: Bearer $VIBED_TOKEN" http://localhost:8080/v1/apps

# a page of 20, starting at the 40th
curl -H "Authorization: Bearer $VIBED_TOKEN" \
  "http://localhost:8080/v1/apps?limit=20&offset=40"
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
      "last_deployed_at": "2026-07-03T10:12:00Z",
      "reason": "Running",
      "message": "user process listening"
    }
  ],
  "total": 1
}
```

`items` is the requested page; `total` is the number of apps the caller can see **after** owner/authorization filtering — the full count, not the page size — so a client can tell whether more pages remain. With no `limit`/`offset` the whole list is returned and `total` equals the length of `items`. `phase` is one of `Pending`, `Claiming`, `Starting`, `Ready`, `Suspended`, `Failed` (see [App Lifecycle](../concepts/app-lifecycle.md)). Returns `401` without a token.

`reason` and `message` carry the app's Ready condition — a short machine-readable code and a human explanation. On a **failed** app this is often the only diagnosis available: a deploy that fails before a pod exists (no warm pool for the required template, say) produces no logs at all, so `GET /v1/apps/{id}/logs` returns nothing to explain it. Read these before retrying — most such failures are configuration gaps that will fail again identically. Healthy apps carry them too (`Running` / "user process listening"), so treat them as an error only when `phase` is `Failed`.

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

Read logs from the app's runner as a **Server-Sent Events** (SSE) stream. Ownership is checked up front, so a `404` is returned cleanly before the connection is upgraded. By default the endpoint returns a **snapshot** of recent lines and then closes; pass **`?follow=true`** to keep the connection open and stream new lines as they arrive.

```bash
# snapshot (returns, then closes)
curl -N -H "Authorization: Bearer $VIBED_TOKEN" \
  http://localhost:8080/v1/apps/my-tool/logs

# live tail
curl -N -H "Authorization: Bearer $VIBED_TOKEN" \
  "http://localhost:8080/v1/apps/my-tool/logs?follow=true"
```

The response is `text/event-stream`; each log line arrives as one event:

```
data: 2026-07-03T10:12:01Z starting server on :8080

data: 2026-07-03T10:12:02Z listening
```

If the stream fails mid-flight, a terminal `event: error` frame is emitted. A client disconnect (cancelled request) is not an error. Concurrent streams per user are capped by [`limits.maxConcurrentLogStreamsPerUser`](../configuration/config-reference.md); exceeding it returns `429` with a `Retry-After` header. Responses: `200` (stream), `401`, `404`, `429`.

### `GET /v1/apps/{id}/versions`

List an app's deploy history, newest first (the first item is the current deployment). Each successful deploy snapshots the source under a monotonic version number, so any listed version can be restored via [rollback](#post-v1appsidrollback). Ownership is checked; a caller that doesn't own the app gets `404`.

```bash
curl -H "Authorization: Bearer $VIBED_TOKEN" \
  http://localhost:8080/v1/apps/my-tool/versions
```

```json
{
  "items": [
    { "version": 3, "timestamp": "2026-07-08T09:41:00Z", "template": "node-24", "lane": "general", "rolled_back_from": 1 },
    { "version": 2, "timestamp": "2026-07-07T18:02:00Z", "template": "node-24", "lane": "general" },
    { "version": 1, "timestamp": "2026-07-07T11:20:00Z", "template": "node-24", "lane": "general" }
  ]
}
```

Each entry also carries a `source_hash`; `rolled_back_from` is present only on a version a rollback created (above, v3 restored v1's source). History is retained up to a fixed cap (older versions are pruned as new ones land). Responses: `200`, `401`, `404`.

### `POST /v1/apps/{id}/rollback`

Redeploy an app from a previous version's snapshotted source. The rollback itself becomes a new version at the head of the history (it does not rewrite it). Ownership is checked.

```bash
curl -X POST -H "Authorization: Bearer $VIBED_TOKEN" \
  -H "Content-Type: application/json" -d '{"version": 1}' \
  http://localhost:8080/v1/apps/my-tool/rollback
```

Returns the same `App` shape as a deploy: `200` when the app is `Ready` within the latency budget, `202` with a `status_url` to poll otherwise. Responses: `200`, `202`, `400` (unknown version), `401`, `404`.

## Share links

A **share link** is a public, optionally password-protected URL that lets an account-less viewer see an app's name, status, and URL. Creating and listing links is owner-scoped and lives under `/v1`; resolving and revoking a link are served under `/api` (the resolve route is deliberately unauthenticated so a recipient without a vibeD account can open it).

Share links require a durable store (the SQLite backend); with the in-memory/configmap backends these routes are unavailable.

### `POST /v1/apps/{id}/share-links`

Create a share link for an app the caller owns.

```bash
curl -X POST -H "Authorization: Bearer $VIBED_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"password": "s3cret", "expires_in": "7d"}' \
  http://localhost:8080/v1/apps/my-tool/share-links
```

`password` and `expires_in` are optional; an empty `expires_in` means no expiry, and a non-empty value that isn't a valid Go duration is rejected with `400`. The response is a `ShareLink` (`token`, `has_password`, `expires_at`, `url`, …). Responses: `200`, `400`, `401`, `404`.

### `GET /v1/apps/{id}/share-links`

List an app's share links (owner or admin only). Returns `{items, total}`. Responses: `200`, `401`, `404`.

### `GET /api/share/{token}` · `POST /api/share/{token}`

**Public** resolution of a share link — no bearer token. `GET` returns the app's name, status, and URL; a password-protected link returns `401` until the password is supplied via `POST` with `{"password": "..."}`. Expired or revoked links return `404` (no information leakage). This route is intentionally exempt from the auth middleware.

### `DELETE /api/share-links/{token}`

Revoke a share link by token (owner or admin). The link returns `404` afterwards. Responses: `204`/`200`, `401`, `404`.

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

It sits behind the same auth middleware as `/v1`, so only the shared agent token can pull. The `{id}` is a **capability**, not a guessable name: since the agent authenticates with one shared token there is no per-user identity to authorize against, so each key carries 128 bits of entropy (`myapp.v3.<32 hex>`) and possession of the reference is the authorization. Keys are minted at deploy time and stored on the app, never re-derived. Sources uploaded before v0.6.0 keep their older, name-derived keys until version retention evicts them. In production the `served` backend is not used: sandboxes have no cluster DNS or cluster-internal egress under the restrictive NetworkPolicy, so the agent instead pulls from a pre-signed **S3** URL and vibeD serves nothing here. See [Storage](../configuration/storage.md) for `served` vs `s3`.

## Other `/api` endpoints

The original REST surface under **`/api/artifacts*`** and **`/api/targets`** were **removed in v0.6**; the artifact lifecycle now lives entirely under `/v1/apps`. See [Migrating to v0.6](../migrating-to-v0.6.md) for the endpoint mapping.

These `/api` routes remain — they have no `/v1` equivalent:

| Path                          | Purpose                                                        |
| ----------------------------- | ------------------------------------------------------------- |
| `GET`/`POST` `/api/share/{token}` | Public [share-link](#share-links) resolution (unauthenticated) |
| `DELETE /api/share-links/{token}` | Revoke a [share link](#share-links)                       |
| `/api/events`                 | Dashboard SSE stream of app lifecycle events                  |
| `/api/auth`                   | Public: active auth mode + browser login URL (see below)      |
| `/api/whoami`                 | Authenticated caller's identity                              |
| `/api/users`, `/api/departments` | Admin user/department management                          |

### `GET /api/auth`

Reports which auth mode the server runs and, for modes with a browser login
flow, where that flow starts. **Unauthenticated by design** — a login screen
needs the mode *before* it can authenticate, and the response reveals nothing
the login endpoints don't already.

```bash
curl http://localhost:8080/api/auth
```

```json
{ "enabled": true, "mode": "saml", "loginUrl": "/saml/login" }
```

`loginUrl` is empty for bearer-style modes (`apikey`, `oidc`), where the client
supplies a token or sits behind an authenticating proxy. The dashboard uses
this to send users to SSO instead of showing an API-key prompt that could not
work in that mode.

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
