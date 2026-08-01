# Changelog

All notable changes to vibeD are recorded here. The format loosely follows
[Keep a Changelog](https://keepachangelog.com/); each release is also a signed
git tag, and the Helm chart's `appVersion` tracks the release.

## v0.6.0 — unreleased

A platform performance pass and the consolidation of the API onto `/v1`. It
trims per-request work across the hot paths — auth, controller, deploy, router,
egress, and the state stores — and firms up a stable, observable `/v1` surface
with pagination and deploy-latency metrics.

**Breaking**: now that the `/v1` deploy path fully covers the artifact
lifecycle, the legacy orchestrator and its `/api/artifacts*` REST surface are
removed, along with a few legacy-only capabilities; ~4k lines of legacy code
(the orchestrator plus the deployer, environment, and source-storage packages)
are deleted. The migration guide (`docs/docs/migrating-to-v0.6.md`) maps every
removed route and MCP tool to its `/v1` equivalent.

### Added
- **`/v1/apps` pagination**: `GET /v1/apps` accepts optional `?limit=` and
  `?offset=` and returns `{items, total}`; with no params it returns all apps
  unchanged (`total` is the post-authorization-filter count).
- **Deploy-latency metric**: a Prometheus histogram
  `vibed_deploy_duration_seconds`, recorded on the live (`/v1`) deploy path with
  `{status, target}` labels (`target` = template name; buckets 1/2/5/10/30/60s).
- **SSE lifecycle events for live deploys**: `/v1` deploys now publish
  `artifact.status_changed` / `artifact.deleted` onto the dashboard event bus,
  so the UI no longer has to poll for status.
- **`auth.identityCacheTTL`** (Go duration, default `30s`, `0` disables): caps
  how long a resolved user identity (role / status / department) is cached.
- **`/v1` share links**: `POST /v1/apps/{id}/share-links` (create) and
  `GET /v1/apps/{id}/share-links` (list) move public share-link management onto
  the `/v1` surface. The public resolve (`GET /api/share/{token}`) and revoke
  (`DELETE /api/share-links/{token}`) routes are unchanged.
- **Additive extension points**: optional `authz.ListScoper` and
  `authz.BatchAuthorizer` interfaces (the `Authorizer` seam itself is unchanged)
  and a streaming `policy.Input.SourceOpener` accessor (the `Source []byte`
  field is retained).

### Changed
- **Static API keys are resolved once at startup** (`file:` / `env:` secrets):
  rotating a key file or env var now requires a restart, rather than being
  re-read on every request.
- **Request identity is resolved once per request**, bounded by
  `auth.identityCacheTTL`: per-request user-store queries drop from 2–5 to ~1,
  and a suspension or role change propagates within the TTL on a warm cache
  (`0` = immediate, as before).
- **MCP lifecycle tools now act on live apps**: `list_versions`,
  `rollback_artifact`, `get_artifact_logs`, and the share-link tools operate on
  warm-pool (live) apps.
- **SQLite state store**: the connection pool is now bounded and per-connection
  pragmas (including `busy_timeout`) are applied via the DSN, so they take on
  every pooled connection.
- **Controller**: the informer cache is scoped to vibeD-managed objects,
  injection runs asynchronously so reconcile workers are never parked, and
  readiness is detected via a watch instead of polling every 250ms.
- **Deploy path**: streams the source end-to-end at constant memory when no
  policy gate is registered, and in-sandbox source adoption is now a rename
  rather than a copy.
- **Router** skips Caddy updates when no routing-relevant field changed.
- **Egress authz** uses an indexed pod-IP lookup for the per-request check.
- **Garbage collection is now a `VibedApp`-CR-keyed `SandboxClaim`-orphan
  backstop only.** Live resources are owner-referenced to their `VibedApp` and
  cascade-deleted by Kubernetes, so in the normal case there is nothing to
  collect. The GC only reaps a `SandboxClaim` whose owning `VibedApp` is gone
  and whose owner-ref cascade did not fire, once the claim is older than
  `gc.maxAge` — releasing the stranded warm-pool pod back to the pool.
  `gc.enabled`, `gc.interval`, `gc.maxAge`, and `gc.dryRun` are unchanged.

### Removed
- **The legacy orchestrator and its `/api/artifacts*` REST surface.** The
  artifact lifecycle now lives entirely under `/v1/apps`; the migration guide
  (`docs/docs/migrating-to-v0.6.md`) maps every removed route to its `/v1`
  equivalent. Still current: `/api/share/`, `/api/events` (SSE), and the admin
  `/api/users`, `/api/departments`, `/api/whoami` routes.
- **`/api/targets` and the `list_deployment_targets` MCP tool.** The
  deployment-target/backend model they exposed is superseded by lanes and
  templates.
- **User-grant sharing**: the `share_artifact` / `unshare_artifact` MCP tools and
  the `/api/artifacts/{id}/share` and `/api/artifacts/{id}/unshare` endpoints.
  Public share links (create/list on `/v1/apps/{id}/share-links`) replace it.

### Fixed
- **MCP tools were broken for warm-pool apps**: `list_versions`,
  `rollback_artifact`, `get_artifact_logs`, and the share-link tools only saw
  legacy-orchestrator artifacts and missed live apps.
- **Live deploys emitted no SSE events**, so the dashboard could only track
  their status by polling.
- **Runner agent**: caller context deadlines now govern agent calls (no
  truncated timeouts), and response bodies are drained so connections are
  reused rather than discarded.
- **SQLite per-connection pragmas** were previously applied to only one pooled
  connection; they now apply to every connection via the DSN.
- **API-key user provisioning** retries until it durably succeeds, instead of
  failing to persist and leaving the API-key user missing.
- **Event bridge** reconciles its state on reconnect and publishes display
  status, so status is not lost across a dropped connection.
- **Source classifier** no longer misclassifies uploads because of macOS archive
  cruft (`__MACOSX/` entries and `._*` AppleDouble files).

## v0.5.2 — 2026-07-19

A hardening and team-visibility release. It closes the entire sev-labeled issue
backlog — performance, correctness, and security fixes across the deploy path,
controller, quota, rate limiting, and the state stores — adds a pluggable
authorizer for team-scoped app visibility and SCIM-ready public routes, and
rolls up the security patch that was staged as v0.5.1 (never tagged).

### Added
- **Authorizer seam + team visibility**: a per-action `RegisterAuthorizer` seam
  and an authorizer-scoped app List, so a viewer can see the team's apps rather
  than only their own; plus an authenticated HTTP-route seam. (#113, #21)
- **SCIM-ready public HTTP routes** and OIDC provisioning hardening toward SAML
  parity. (#116, #17)
- **Rate limiting now covers `/v1/*`** (including the deploy path) with a
  separate, stricter per-client budget for mutating verbs
  (`rateLimit.writeRequestsPerSecond` / `rateLimit.writeBurst`); it also warns at
  startup when disabled on a network transport. (#50)
- **Global log-stream cap** `limits.maxConcurrentLogStreamsGlobal` alongside the
  per-user cap. (#81)
- **List pagination** (`?limit=&offset=`) for the users, departments, and
  share-link APIs. (#80)

### Changed
- Helm chart `version` / `appVersion` → **0.5.2**.
- Auth-disabled mode treats every request as admin, so vibeD now **refuses to
  start with auth disabled on a non-loopback bind** unless `auth.devInsecure:
  true` is set (the chart's no-auth path sets it automatically). (#55)
- `limits.maxConcurrentLogStreamsPerUser` `<=0` now falls back to a safe default
  instead of meaning unlimited. (#81)
- Quota counts against the exact `spec.owner`, not a lossy sanitized label, so
  colliding identities no longer share a quota bucket. (#66)

### Fixed
- **HTTP 502 on warm-pool adoption**: the per-app Service selected the
  `agents.x-k8s.io/claim-uid` label that agent-sandbox v0.5 filters off the
  adopted pod; it now selects `sandbox-name-hash`. (#42)
- **Dashboard login form** never appeared behind an HTTP/2 gateway (blank
  `Response.statusText`); the client now branches on the numeric 401. (#41)
- Quota List-then-Create overshoot under concurrent deploys, closed with an
  in-process reservation ledger. (#75)
- The app List and the log-stream pod lookup no longer scan the whole namespace
  (owner-label pre-filter + a `status.podIP` field selector). (#74, #77)

### Performance
- O(1) circular log ring buffer, replacing an O(capacity)-per-line shift. (#79)
- Bounded-sample rate-limiter eviction, replacing an O(n) scan under the write
  lock. (#62)
- Controller uses capped exponential backoff for transitional requeues instead
  of a flat 2s hot-poll. (#70)
- GC paginates its list sweeps and deletes with bounded concurrency. (#76)
- Deploy hot path hashes the source during the read pass (one fewer full pass
  over the up-to-50MB tarball). (#73)
- SQLite: `created_at` and partial `api_key_hash` indexes, plus an indexed
  `shared_with` join table replacing a `LIKE` full-table scan. (#80)
- ConfigMap store: lock-free reads and optimistic-concurrency writes (no
  process-wide lock held across API I/O), plus a pre-write size guard that fails
  with an actionable error instead of bricking at etcd's ~1MB ceiling. (#71, #72)

### Security
- Rolls up the **v0.5.1 security patch**: dependency bumps closing all
  critical/high advisories (`golang.org/x/crypto`, `x/net`, the npm docs chain,
  …), CodeQL-flagged hardening (bounded audit allocation, workerd path
  containment, source-fetch fail-closed), and least-privilege GitHub Actions
  permissions. (#110)
- The no-auth startup guard (#55) and stricter deploy-path rate limiting (#50)
  reduce accidental exposure of an unauthenticated control plane.

### Docs
- Config reference documents `rateLimit.writeRequestsPerSecond` / `writeBurst`,
  `limits.maxConcurrentLogStreamsGlobal`, and `auth.devInsecure`; corrects the
  per-user log-stream `<=0` semantics; and notes the ConfigMap backend's ~1MB
  scale bound (use `sqlite` at scale).
- Authentication guide documents the no-auth non-loopback-bind guard.

## v0.5.0 — 2026-07-09

The `/v1` app data plane reaches parity with the dashboard — version history &
rollback, live logs, deploy/MCP metrics, and public share links — alongside
source-fetch hardening and an install-docs overhaul.

### Added
- **Version history & rollback** for `/v1` apps: every deploy snapshots its
  source under a monotonic version; `GET /v1/apps/{id}/versions` lists the
  history and `POST /v1/apps/{id}/rollback` redeploys a prior version (retained
  up to a fixed cap). (#99)
- **Live log streaming** on `/v1`: `GET /v1/apps/{id}/logs` returns a snapshot
  by default and streams with `?follow=true`; the dashboard tails it. (#98)
- **Deploy & MCP metrics**: Prometheus `deploys_total`, `artifacts_active`, and
  `tool_calls_total`, powering the dashboard's Deploy Success Rate, Active
  Artifacts, and MCP Tool Usage panels. (#97)
- **Public share links** for `/v1` apps: password-optional, revocable links that
  resolve to an app's URL. (#100)
- **Warm-pool make targets** `enable-go-pool` and `enable-base-pool` for the
  `go-123` / `base-al2023` templates. (#96)

### Security
- **Source-fetch hardening** in the runner agent: an SSRF dial guard (blocks
  link-local / loopback / cloud-metadata addresses, including the IPv6 IMDS) and
  decompression-bomb caps that count bytes *written* against a shared budget, so
  PAX sparse-file entries can't slip past. (#94)

### Changed
- Helm chart gains a `warmPoolsEnabled` master switch so a first `helm install`
  succeeds before the agent-sandbox **extensions** CRDs are present — set it
  `false`, then flip it on `helm upgrade`. Chart `version`/`appVersion` → 0.5.0.
  (#95)
- The dashboard profile menu now auto-closes on mouse-leave. (#96)

### Docs
- Installation guide split into a dev path (`make dev`, no S3) and a production
  path, with agent-sandbox core-vs-extensions + version guidance and the
  CRD-ordering fix; **Local Development** moved first in the nav. (#95)
- Goose MCP extension setup; a **manual test runbook** (runtimes × configs); and
  the `/v1` versions/rollback endpoints plus log `?follow` documented in the
  HTTP API reference. (#96, #101)

### Upgrading
- **Backward-compatible.** The public Go surface (`pkg/api`, `pkg/plugin`,
  `pkg/server`) is unchanged and the new `/v1` endpoints are additive — no code
  or config migration. A `helm upgrade` picks up the 0.5.0 images.
- Installing onto a cluster **without** the agent-sandbox extensions CRDs? Use
  `--set warmPoolsEnabled=false`, then enable pools once the CRDs are present.

## v0.4.5 — 2026-07-07
- Config validator now consults the plugin registry (#92): out-of-tree store
  backends / auth modes registered via `pkg/plugin` validate correctly,
  completing the plugin extension surface.
- Documentation website refresh (#93).

## v0.4.4 — 2026-07-06 (security release)
- Egress SSRF / DNS-rebind hardening (metadata endpoint blocked by resolved IP;
  IPv6 NetworkPolicy ranges); static-API-key suspension enforcement via a
  canonical user ID; fail-closed artifact ownership when auth is enabled; plus a
  batch of smaller security and performance fixes.
  (#46, #57, #60, #59, #48, #63, #49, #54, #58, #53, #65, #64, #68, #69, #67, #78)
- **Note:** #48 changes the static-key ownership identity to `apikey-<name>` —
  see `docs/configuration/authentication.md` before upgrading.

_Earlier releases (v0.4.3 and before) are recorded in their git tags._
