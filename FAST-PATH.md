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
| 0.3 | ☐ | `vibed-runner-python` image: interpreter + pre-baked deps + agent as PID 1 | `runners/python/` (new), `Makefile` |
| 0.4 | ☐ | `vibed-runner-node` image: same shape | `runners/node/` (new), `Makefile` |
| 0.5 | ☐ | Pre-baked deps manifest format + per-image manifest files | `runners/*/prebaked.yaml` |
| 0.6 | ☐ | `make runner-images` target + CI to build/push to cluster registry | `Makefile`, CI config |

### Phase 1 — Warm pool manager

| # | Status | What | Where |
|---|--------|------|-------|
| 1.1 | ☐ | `internal/pool` package: `Claim` / `Release` / `Recycle` | `internal/pool/pool.go` (new) |
| 1.2 | ☐ | Replenishment loop — top pool up ahead of demand | `internal/pool/` |
| 1.3 | ☐ | Health + age eviction of idle runners | `internal/pool/` |
| 1.4 | ☐ | Recycle-never-reuse: destroy + replace pods that ran user code | `internal/pool/` |
| 1.5 | ☐ | Cold-runner fallback when pool exhausted | `internal/pool/` |
| 1.6 | ☐ | Wire pool manager into `main.go` as lifecycle-context goroutine | `cmd/vibed/main.go` |
| 1.7 | ☐ | Pool metrics: size, claim latency, exhaustion rate | `internal/pool/`, metrics package |

### Phase 2 — RunnerDeployer + code injection

| # | Status | What | Where |
|---|--------|------|-------|
| 2.1 | ☐ | Agent control-channel client (inject, restart, logs, health) | `internal/deployer/` or `internal/runner-agent/client` |
| 2.2 | ☐ | `RunnerDeployer` implementing the `Deployer` interface | `internal/deployer/runner.go` (new) |
| 2.3 | ☐ | Register `RunnerDeployer` in the factory | `cmd/vibed/main.go:211` |
| 2.4 | ☐ | Authenticated, in-cluster-only injection channel | agent + deployer |

### Phase 3 — Orchestrator fast-path branch + dependency gate

| # | Status | What | Where |
|---|--------|------|-------|
| 3.1 | ☐ | Dependency gate: scan deps vs pre-baked manifest | `internal/orchestrator/` or `internal/environment/` |
| 3.2 | ☐ | "Runner shortcut" in `doDeploy` parallel to the static shortcut | `internal/orchestrator/orchestrator.go:~413` |
| 3.3 | ☐ | `Mode` field on artifact (`preview` \| `built`) | `internal/api/`, store schema |
| 3.4 | ☐ | Surface `Mode` in `status` / `list` MCP tools + dashboard | `internal/mcp/`, frontend |

### Phase 4 — Promote

| # | Status | What | Where |
|---|--------|------|-------|
| 4.1 | ☐ | `promote_artifact` MCP tool | `internal/mcp/` |
| 4.2 | ☐ | Promote pipeline: build → deploy built image → swap → release runner | `internal/orchestrator/` |
| 4.3 | ☐ | Optional `autoPromote` config behavior | `internal/orchestrator/`, config |
| 4.4 | ☐ | Decide + implement URL stability across the swap (ingress alias?) | `internal/deployer/` |

### Phase 5 — GC, config, docs, hardening

| # | Status | What | Where |
|---|--------|------|-------|
| 5.1 | ☐ | GC: short max-age for `Mode==preview`; recycle pooled runners on collection | `internal/gc/collector.go` |
| 5.2 | ☐ | `FastPath` config block (enabled, languages, per-lang pool size/image/manifest, namespace, resources, preview TTL, autoPromote) | `internal/config/config.go` |
| 5.3 | ☐ | Helm `values.yaml` + `configmap.yaml` for `FastPath` | `deploy/helm/vibed/` |
| 5.4 | ☐ | Verify `agents.x-k8s.io` RBAC sufficient for pool churn (create/delete/list) | `deploy/helm/vibed/templates/rbac.yaml` |
| 5.5 | ☐ | Docs: instant preview, promote, runner images | `docs/docs/` |
| 5.6 | ☐ | Load test + fast-vs-fallback / preview-vs-promote metrics dashboard | `testbed/observability/` |

## Risks / open questions

- **Runner agent is new attack surface** — lives in a pod running untrusted user
  code. Control channel must be one-directional, authenticated, not publicly
  reachable. (Phase 2.4)
- **Idle resource cost** — K warm pods per language. Needs sane defaults and
  demand-based pool autoscaling. (Phase 1.2)
- **Preview sprawl** — previews must be GC'd aggressively or the pool/cluster
  fills. (Phase 5.1)
- **Promote URL stability** — does the URL change preview → built? May need an
  ingress alias. (Phase 4.4)
- **Knative deliberately not in the fast path** — its revision model resists code
  injection; it stays the home for promoted, built images.

## Implementation order

Phase 0 → 1 → 2 → 3 → 4 → 5. Phases 0–2 are independently testable (claim a pod,
inject a hello-world by hand) before the orchestrator wiring in Phase 3.

## Working notes

### Implementation log

_(empty — add dated entries as items land; note surprises here.)_
