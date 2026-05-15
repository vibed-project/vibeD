# vibeD Instant Preview — Design & Task Plan

Working document for the `v0.3.0` fast-path feature: deploy "little code apps" in
sub-second time by skipping the per-request container build. File-and-line
references are accurate as of the design audit; update them as code lands.

## Headline

**The Buildah Kubernetes Job is the latency tax.** It exists only to bake source
into an OCI image. Local tools (Goose, Claude Desktop) feel instant because they
never build a container — they run code in a runtime that already exists. This
plan brings that to central K8s: keep a **warm pool of idle runner pods** on
generic language images, **inject source over a control channel** at request
time, and run it. The result is an ephemeral **preview**; a separate **promote**
step runs the real Buildah build for a durable, digest-pinned artifact.

The existing static fast path (`orchestrator.go` static shortcut → ConfigMap +
`nginx-unprivileged`, no build) is the proof of concept. This generalizes it to
Python and Node.

## Decisions locked

| Decision | Choice |
|---|---|
| Dependency scope | **Zero-dep / pre-baked only.** Anything with deps not in the runner image's pre-baked manifest falls back to the Buildah build path. |
| Warm vs cold | **Warm pool now.** Idle pods claimed per deploy; replenished ahead of demand. Cold runner is the exhaustion fallback. |
| Durability | **Preview → promote.** Fast path is an ephemeral preview; explicit/auto promote runs the real build. |
| Backend | **Sandbox CRD** (`agents.x-k8s.io`) — natural home for warm, long-lived runner pods. |

## End-state flow

```
deploy_artifact (python/node, no external deps)
  └─ doDeploy: dependency gate passes → target = "runner"
       └─ RunnerDeployer.Deploy
            ├─ pool.Claim(language)        → idle Sandbox, agent already up
            ├─ agent.Inject(files, cmd, env) over control port
            ├─ agent starts user process, reports healthy
            └─ return Sandbox URL          (artifact.Mode = "preview")   ← sub-second

promote_artifact (or auto-promote)
  └─ builder.Build (Buildah Job) → digest-pinned image
       └─ deploy to Knative/K8s, swap, release pooled runner   (artifact.Mode = "built")
```

## Pipeline map (target)

`MCP deploy_artifact` → `Orchestrator.AsyncDeploy` (`internal/orchestrator/orchestrator.go:183`)
→ goroutine `doDeploy` (`:289`) → **dependency gate** →
  - **fast path:** `RunnerDeployer.Deploy` → `pool.Claim` → agent inject → ready
  - **build path:** `builder.Build` (`internal/builder/buildah.go:72`) → `Factory.Get(target)` → existing deployers

`promote_artifact` → `builder.Build` → deploy built image → swap → `pool.Release`.

## Components

### Runner images
Pre-built `vibed-runner-python`, `vibed-runner-node`: interpreter + curated
**pre-baked common deps** + the runner agent as PID 1. Built once via
`make runner-images` + CI, pushed to the cluster registry — never per request.
Each image ships a **pre-baked deps manifest** consumed by the dependency gate.

### Runner agent
Small Go binary, PID 1 in the runner pod. Exposes an **in-cluster-only control
port** accepting a source bundle + run command + env; writes files to a workdir,
execs and supervises the user process, captures logs, reports health. The user
app serves on the **app port** (the Sandbox's exposed URL). Entrypoint detection
(`main.py`/`app.py`, `package.json` main/`index.js`) reuses logic in
`internal/builder/dockerfile.go`.

### Warm pool manager (`internal/pool`, new)
Maintains N idle Sandbox CRs per language. `Claim(ctx, lang)`, `Release`,
`Recycle`; replenishment loop tops the pool up ahead of demand; health/age
eviction. **Recycle, never reuse** — once a pod has run user code it is destroyed
and replaced (isolation). Pool exhausted → cold runner deploy fallback so deploys
never hard-fail. Started as a lifecycle-context-aware goroutine in `cmd/vibed/main.go`.

### RunnerDeployer (`internal/deployer/runner.go`, new)
Implements the `Deployer` interface (`internal/deployer/deployer.go:14`), registered
in the factory (`cmd/vibed/main.go:211`). `Deploy` = claim + inject + wait-healthy
+ URL. `Update` = re-inject into the same runner. `Delete` = release to pool.
`GetLogs` = from the agent. Injection channel: vibeD → agent control port via pod
IP / Sandbox service DNS, authenticated, in-cluster only. (`pods/exec` considered
and rejected — extra RBAC, slower updates.)

### Dependency gate + orchestrator branch
New "runner shortcut" in `doDeploy` parallel to the static shortcut
(`orchestrator.go:~413`): language ∈ {python, node} AND gate passes AND fast-path
enabled → skip `builder.Build()`, target = `"runner"`. Gate scans
`requirements.txt` / `package.json` / imports against the runner image's pre-baked
manifest; any miss → fall back to build path. New `Mode` field on the artifact
(`preview` | `built`).

### Promote
New MCP tool `promote_artifact` (+ optional `autoPromote` config). Runs the normal
Buildah build, deploys the digest-pinned image to Knative/K8s, swaps the live
deployment, flips `Mode` to `built`, releases the pooled runner. Artifact ID stays
stable.

## Task plan

### Phase 0 — Runner images + runner agent (foundation)

| # | Status | What | Where |
|---|--------|------|-------|
| 0.1 | ☑ | Runner agent: control port, source-bundle ingest, process supervise, log capture, health report | `internal/runneragent/` + `cmd/vibed-runner-agent/` (new) — HTTP control API (`/inject`, `/status`, `/logs`, `/stop`, `/healthz`), bearer-token auth, generation-guarded process supervision, line-oriented log ring buffer, path-traversal-safe file writes |
| 0.2 | ☑ | Entrypoint detection shared with builder (`main.py`/`app.py`, `package.json` main/`index.js`) | new `internal/appspec` package — `DetectLanguage`, `Entrypoint`, `RunCommand`, `Interpreted`; consumed by `internal/builder/dockerfile.go` + `internal/orchestrator` |
| 0.3 | ☑ | `vibed-runner-python` image: interpreter + pre-baked deps + agent as PID 1 | `runners/python/{Dockerfile,prebaked.yaml}` — python:3.12-slim, flask/fastapi/uvicorn/gunicorn/requests/jinja2/pydantic pre-baked, agent under tini, non-root UID 1000. Built + smoke-tested (216 MB). |
| 0.4 | ☑ | `vibed-runner-node` image: same shape | `runners/node/{Dockerfile,prebaked.yaml}` — node:22-bookworm-slim, express/cors pre-baked via NODE_PATH, agent under tini, non-root UID 1000. Built + smoke-tested (272 MB). |
| 0.5 | ☑ | Pre-baked deps manifest format + per-image manifest files | new `internal/prebaked` package (`Manifest`, `Load`/`Parse`, normalized `Has`); `runners/{python,node}/prebaked.yaml` |
| 0.6 | ☑ | `make runner-images` target + CI to build/push to cluster registry | `Makefile` (`runner-images`, `runner-image-{python,node}`, `load-runner-images`); `.github/workflows/runner-images.yaml` (multi-arch matrix → GHCR) |

### Phase 1 — Warm pool manager

| # | Status | What | Where |
|---|--------|------|-------|
| 1.1 | ☑ | `internal/pool` package: `Claim` / `Release` (recycle) | `internal/pool/pool.go`, `runner.go` (new) — `Claim` warm-or-cold, `Release` deletes the Sandbox CR. Plus `FastPathConfig`/`RunnerConfig` in `internal/config`. |
| 1.2 | ☑ | Replenishment loop — top pool up ahead of demand | `internal/pool/pool.go` — `replenish` brings idle+pending up to pool size; runs on a ticker in `Run` and async after every `Claim`/`Release`. |
| 1.3 | ☑ | Health + age eviction of idle runners | `internal/pool/pool.go` — `sweep` recycles idle runners past `MaxIdleAge` or failing the agent health probe; claim-in-flight runners are left untouched. |
| 1.4 | ☑ | Recycle-never-reuse: destroy + replace pods that ran user code | `internal/pool/pool.go` — `Release` deletes the Sandbox outright; replenish refills with a fresh pod. |
| 1.5 | ☑ | Cold-runner fallback when pool exhausted | `internal/pool/pool.go` — `Claim` creates + waits for a cold runner when the idle pool is empty, so deploys never fail on a spike. |
| 1.6 | ☑ | Wire pool manager into `main.go` as lifecycle-context goroutine | `cmd/vibed/main.go` — `pool.New` + `go runnerPool.Run(lifeCtx)` when `fastPath.enabled`. |
| 1.7 | ☑ | Pool metrics: idle size, claim latency, warm/cold split, warmup outcomes | `internal/metrics/metrics.go` — `vibed_pool_*` runners_idle / claims_total / claim_duration / runners_created. |

### Phase 2 — RunnerDeployer + code injection

| # | Status | What | Where |
|---|--------|------|-------|
| 2.1 | ☑ | Agent control-channel client (inject, status, logs, stop, health) | `internal/runneragent/client.go` — bearer-token HTTP client; tested end-to-end against a real `Agent`. |
| 2.2 | ☑ | `RunnerDeployer` implementing the `Deployer` interface | `internal/deployer/runner.go` (new) — `Deploy` claims a runner + injects source + confirms the process stays up; `Update` re-injects; `Delete` recycles; reads source via `storage.GetSourcePath`. Pool + agent are interfaces, unit-tested with stubs. |
| 2.3 | ☑ | Register `RunnerDeployer` in the factory | `cmd/vibed/main.go` — `api.TargetRunner` registered when `fastPath.enabled`. |
| 2.4 | ☑ | Authenticated, in-cluster-only injection channel | `fastPath.agentToken` (auto-generated if unset) → injected into runner pods as `VIBED_AGENT_TOKEN` by the pool, presented as a bearer token by the deployer's client. |

### Phase 3 — Orchestrator fast-path branch + dependency gate

| # | Status | What | Where |
|---|--------|------|-------|
| 3.1 | ☑ | Dependency gate: scan deps vs pre-baked manifest | `internal/prebaked/gate.go` + `registry.go` — `requirements.txt`/`package.json` parsers + `Eligible()`; manifests moved to `internal/prebaked/manifests/*.yaml` (single source of truth, `go:embed`'d into vibeD AND `COPY`'d into the runner images). |
| 3.2 | ☑ | "Runner shortcut" in `doDeploy` parallel to the static shortcut | `internal/orchestrator/orchestrator.go` — `fastPathEligible` + `deployRunner` in `doDeploy`, `updateRunner` in `doUpdate`; explicit `target: runner` that fails the gate errors clearly. |
| 3.3 | ☑ | `Mode` field on artifact (`preview` \| `built`) | `pkg/api/types.go` — `DeployMode` + `Mode` on `Artifact` and `ArtifactSummary`; set by the orchestrator (`ModePreview` on the runner path, `ModeBuilt` otherwise). |
| 3.4 | ☑ | Surface `Mode` in `status` / `list` MCP tools + dashboard | MCP tools return `api.Artifact`/`ArtifactSummary` directly, so `Mode` flows through automatically; dashboard shows a `preview` badge + Instant Preview / Sandbox target labels (`web/src/`). |

### Phase 4 — Promote

| # | Status | What | Where |
|---|--------|------|-------|
| 4.1 | ☑ | `promote_artifact` MCP tool | `internal/mcp/tools_promote.go` — `orch.AsyncPromote`; returns "building", poll status. |
| 4.2 | ☑ | Promote pipeline: build → deploy built image → swap → release runner | `internal/orchestrator/orchestrator.go` — `doPromote`: build from stored source → `SelectTarget` → deploy digest-pinned image → release pooled runner → flip `Mode` to `built`. On *any* failure the preview is restored and left running. `canPromote` guard unit-tested. |
| 4.3 | ☑ | Optional `autoPromote` config behavior | `config.FastPathConfig.AutoPromote` — `deployRunner` fires `autoPromote` in the background after a successful preview; panic-recovered, lifecycle-context-scoped. |
| 4.4 | ☑ | Decide URL stability across the swap | **Decided: artifact ID is stable, URL is not.** Promote swaps from the runner's address to the durable backend's URL; callers re-fetch via `get_artifact_status`. No ingress alias — see risks (external reachability of previews is a separate, pre-existing concern). |

### Phase 5 — GC, config, docs, hardening

| # | Status | What | Where |
|---|--------|------|-------|
| 5.1 | ☑ | GC: short max-age for `Mode==preview`; recycle pooled runners on collection | `internal/gc/collector.go` — `cleanStalePreviews` reaps `Mode==preview` artifacts older than `previewTTL` via a `PreviewReaper` (the orchestrator's new `ReapArtifact`, which routes through `RunnerDeployer.Delete` → `pool.Release`). `Delete` refactored into a shared `deleteArtifact`. |
| 5.2 | ☑ | `FastPath` config block | `internal/config/config.go` — full block landed across Phases 1–5: enabled, namespace, replenish/ready/idle timings, per-language runner (image, pool size, ports, resources), `agentToken`, `autoPromote`, `previewTTL`. |
| 5.3 | ☑ | Helm `values.yaml` + `configmap.yaml` for `FastPath` | `deploy/helm/vibed/` — full `config.fastPath` block in `values.yaml` (disabled by default) rendered into `configmap.yaml`, including the per-language `runners` map. `helm template` verified for both off and on. |
| 5.4 | ☑ | Verify `agents.x-k8s.io` RBAC sufficient for pool churn | Verified — the existing `agents.x-k8s.io/sandboxes` rule already grants `get/list/watch/create/update/delete`, which covers pool churn (create/delete), the GC sweep (list/delete), and the SandboxDeployer. Comment in `rbac.yaml` updated to note the pool. |
| 5.5 | ☑ | Docs: instant preview, promote, runner images | `docs/docs/concepts/instant-preview.md` + `mcp-tools/promote-artifact.md` (new), `config-reference.md` `fastPath` block + env vars, `mcp-tools/overview.md` table, `sidebars.js`. |
| 5.6 | ◑ | Metrics dashboard panels (load test deferred) | `testbed/observability/dashboards/vibed-overview.json` — new "Fast Path" row: Idle Runners, Runner Claims (warm vs cold), Claim Latency P99. **Load test deferred** — needs the full fast-path stack on a live cluster; a manual follow-up. |

## Risks / open questions

- **Runner agent is new attack surface** — lives in a pod running untrusted user
  code. Control channel must be one-directional, authenticated, not publicly
  reachable. (Phase 2.4)
- **Idle resource cost** — K warm pods per language. Needs sane defaults and
  demand-based pool autoscaling. (Phase 1.2)
- **Preview sprawl** — previews must be GC'd aggressively or the pool/cluster
  fills. (Phase 5.1)
- **Promote URL stability** — RESOLVED (4.4): artifact ID is stable, URL is not;
  callers re-fetch via `get_artifact_status` after promote. No ingress alias.
- **External reachability of previews** — the RunnerDeployer (like the existing
  SandboxDeployer) returns an in-cluster `*.svc.cluster.local` URL. For a user
  to actually *see* a preview from outside the cluster, the runner Sandbox needs
  an ingress/route, a port-forward, or a vibeD-side proxy. This is a pre-existing
  gap shared with Sandbox deploys, not introduced by the fast path — but it
  blunts the "instant preview" value until addressed. Candidate for a follow-up.
- **Knative deliberately not in the fast path** — its revision model resists code
  injection; it stays the home for promoted, built images.

## Implementation order

Phase 0 → 1 → 2 → 3 → 4 → 5. Phases 0–2 are independently testable (claim a pod,
inject a hello-world by hand) before the orchestrator wiring in Phase 3.

## Working notes

### Implementation log

**2026-05-14:** All 6 phases implemented on branch `v0.3.0`. The fast path is
live end-to-end: an eligible Python/Node app skips the build, claims a warm
Sandbox-CRD runner, gets its source injected over the agent control API, and
serves — then can be promoted to a durable build. Disabled by default behind
`fastPath.enabled`.

Surprises / decisions:
- The pre-baked manifests were moved to `internal/prebaked/manifests/*.yaml` as
  a single source of truth — `go:embed`'d into vibeD for the dependency gate
  AND `COPY`'d into the runner images by their Dockerfiles. Avoids drift.
- `gopkg.in/yaml.v3` *does* decode duration strings (`"15s"`) into
  `time.Duration` and *rejects* bare integer nanoseconds — so the helm config
  renders fast-path durations as quoted strings, matching `deployment.readyTimeout`.
- RBAC needed no change: the DEPLOY-STABILITY work already granted the full
  verb set on `agents.x-k8s.io/sandboxes`, which covers pool churn + the GC.
- The deployer and pool are consumed through small interfaces (`runnerPool`,
  `agentClient`, `PreviewReaper`) so each layer unit-tests with stubs.

Deferred / follow-ups:
- **Load test (5.6)** — needs the full stack on a live cluster; not run here.
- **Promote integration test** — `canPromote` is unit-tested; the full
  build→swap→release path wants an integration test alongside the cluster-gated
  `orchestrator_integration_test.go`.

**2026-05-15:** Two production gaps closed.
- **Per-runner pod resource limits** — `RunnerConfig.Resources` (limits +
  requests, cpu + memory) plumbed through the pool and Helm; conservative
  defaults applied when unset.
- **External preview reachability** — new `internal/preview` reverse-proxy
  mounted at `/preview/<artifact_id>/`, registered in `main.go` when the fast
  path is enabled. Auth flows through the standard middleware (`/preview/`
  added to `SkipAuthPaths`'s authed-required set); the handler enforces
  per-artifact ownership via `Orchestrator.Status` before proxying. vibeD's
  `Authorization` / `Cookie` headers are stripped before forwarding;
  `X-Forwarded-Host`/`-Proto`/`-Prefix` are set so prefix-aware frameworks
  reconstruct correct URLs. When `server.baseURL` is set, the orchestrator
  rewrites `artifact.url` to the proxy URL so the dashboard link "just works".
  Documented sub-path caveat for apps emitting absolute paths.
