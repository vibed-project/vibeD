# Changelog

All notable changes to vibeD are recorded here. The format loosely follows
[Keep a Changelog](https://keepachangelog.com/); each release is also a signed
git tag, and the Helm chart's `appVersion` tracks the release.

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
