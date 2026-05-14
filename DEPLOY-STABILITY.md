# vibeD Deploy Stability — Analysis & Fix Plan

Working document for the audit of why GenAI-tool deploys (and bring-your-own-container deploys) keep failing, and the prioritized fix list to address it. File-and-line references are accurate as of the audit; update them as we change code.

## Headline

**BYOC didn't help because BYOC bypasses the build but still hits the same Knative spec / RBAC / readiness defects downstream.** Fixing builds doesn't fix deploys; fixing the deploy spec and RBAC does. Most of the must-fix work is on the deployer side, not the build side.

## Pipeline map

`MCP deploy_artifact` → `Orchestrator.AsyncDeploy` (`internal/orchestrator/orchestrator.go:156`) → goroutine `doDeploy` (`:227-448`) → builder (`internal/builder/buildah.go:70`) **or** static-shortcut `deployStatic` (`:958-1032`) → `pushImage` (`:1397-1410`) → `Factory.Get(target)` → `KnativeDeployer`/`KubernetesDeployer`/`SandboxDeployer.Deploy()` → `waitForReady` → `finalizeDeployment` (`:824-849`).

Store and cluster are never reconciled. Only `gc.GarbageCollector` runs (hourly, 24h max-age) and **only handles Jobs/ConfigMaps/Deployments — not Knative Services or Sandboxes** (a major gap).

## Failure-mode taxonomy

### RBAC (multiple still missing after the ClusterRole promotion)

`deploy/helm/vibed/templates/rbac.yaml` after the Role→ClusterRole promotion is still incomplete:

- **No `agents.x-k8s.io` rules at all** → `SandboxDeployer` (`internal/deployer/sandbox.go`) **silently 403s**. This is the BYOC story — bring-your-own-container that targets Sandbox is broken at RBAC.
- **No `events` read** → `FetchPodLogs` (`internal/orchestrator/helpers.go:70-103`) returns `"(no pods found)"` for never-started pods; vibeD has no way to surface `ImagePullBackOff` / PSA admission rejections.
- **No `revisions`/`routes`/`configurations` read** under `serving.knative.dev` → `waitForReady` only sees the rolled-up Service condition, can't surface the real reason.

### Build pipeline

- **Buildah Job has no podSecurityContext** (`internal/builder/buildah.go:147-184`). Under PSA `restricted` (K8s 1.25+), the Job is rejected outright.
- **`BackoffLimit: 0`** (`:145`) — single transient failure permanently fails the build.
- **`TTLSecondsAfterFinished: 120`** (`:146`) deletes the Job before `get_artifact_logs` can fetch logs.
- **`waitForJob` drops parent ctx** (`:228-264`): `waitCtx := context.WithTimeout(context.Background(), b.timeout)`. MCP client disconnects mid-build → orphaned Job no one reads.
- **`bgCtx := context.Background()`** in the orchestrator goroutine (`orchestrator.go:174,465`) — never cancelled on shutdown. SIGTERM leaks every in-flight deploy.
- **No image digest pinning** (`:338`): `imageBase + "/" + name + ":latest"` — redeploy can pull a different image than the one just pushed.
- **Hard-coded retry without backoff/jitter** (`buildah.go:205`).

### Knative readiness — what's biting BYOC users

- **No `securityContext` on any user workload** — `KnativeDeployer.buildService` (`internal/deployer/knative.go:183-228`), `KubernetesDeployer.Deploy` (`:90-127`), `SandboxDeployer.buildSandboxObj` (`:41-113`) all build containers with empty SecurityContext. The Knative warning we see → workload rejected under PSA `restricted` ≥1.25. The chart sets these only on **vibeD's own pod**, not on what vibeD deploys.
- **`waitForReady` 3-minute hard-coded timeout** (`knative.go:49`, `kubernetes.go:38`) is too short for first-deploy image pulls on cold kind nodes. Source of `ProgressDeadlineExceeded` on the static `nginx:alpine` test.
- **Static path port mismatch potential** — `staticNginxConf` listens on 8080, `deployStatic` only sets `Port=8080` when 0; on Update path some artifacts still have `Port=0`, queue-proxy guesses, fails.
- **No autoscaling annotations** — no `autoscaling.knative.dev/min-scale: "1"` or `initial-scale: "1"` → revision scales to zero before vibeD's `waitForReady` ever sees Ready, hence "Initial scale was never achieved".
- **`Knative.Update` for static = delete+recreate** (`knative.go:111-115`) with `_ = Delete(...)`. 503 window + RBAC errors silently dropped.
- **`nginx:alpine` from Docker Hub** is the static-path image (`orchestrator.go:988`). On cold kind nodes the pull alone can blow the 3-min readiness budget, and `nginx:alpine` writes to `/var/cache/nginx`/`/var/run/nginx.pid` — fails immediately under `readOnlyRootFilesystem`/`runAsNonRoot`.

### State machine / GC

- **AsyncDeploy double-create** (`orchestrator.go:186-218`): creates artifact A, goroutine sees A in `building` state, deletes A, creates artifact B with new ID. The ID returned to the MCP caller is **stale by the time the goroutine runs** — `get_artifact_status` on it returns NotFound. Source of "overwriting stuck/failed artifact with same name on every redeploy" in logs.
- **Status FSM informal** — `updateStatus` overwrites without checking current state. Delayed callbacks clobber newer statuses.
- **GC misses Knative Services and Sandboxes** (`internal/gc/collector.go:98-115`). Every failed Knative deploy leaks the `ksvc` until manual `kubectl delete`.
- **GC `MaxAge` 24h** way too long for build Jobs.
- **`Delete` on artifact with empty Target** (`:686-694`) silently skips deployer cleanup. Build crashed pre-target-selection → previous Knative Service for same name leaks.
- **No panic recovery in `doDeploy` goroutine** (`:207-218,475-486`). Panic kills goroutine, artifact stuck at `building` forever.

### Configuration drift

- **GC config has no Helm values** — `values.yaml` has no `gc:` block at all. Production operators can't tune interval/maxAge.
- **`store.configmap.namespace` defaults to release ns in helm, `vibed-system` in Go** (`config.go:264` vs `configmap.yaml:79`).
- **Pack support removed from main.go but still present in chart** (`values.yaml:81 builder.image: "paketobuildpacks/builder-jammy-base:latest"`). Dead config.
- **`config.deployment.namespace: "default"`** in production values — every artifact lands in `default` regardless of `DepartmentID`. MCP `deploy_artifact` (`tools_deploy.go`) has no department field.

### Observability gaps

- `failArtifact` writes a string `reason` only — no Knative conditions, no pod events, no Job tail logs.
- `level=ERROR` lines hold only `error` — no artifact_id, target, image, namespace, or trace_id correlation.
- No `vibed.dev/trace-id` annotation on the Knative Service / Deployment / Sandbox — re-correlating across store and cluster requires manual grep.

### Architectural smells

- **No worker pool / concurrency cap** — every `deploy_artifact` spawns a goroutine.
- **No idempotency keys** — MCP retry on flaky network creates a second artifact (or via overwrite logic, deletes the first and creates a third).
- **Singleflight not used** for `Detector.Detect()` — every deploy hits Discovery API twice.
- **Unstructured client for Sandbox** with `Update` doing full-spec replace (`sandbox.go:133-148`) — loses controller-set defaults. Use Patch.
- **`pushImage` uses local Docker daemon** (`orchestrator.go:1397-1410`) but vibeD runs in a pod without one. Dead code path; the `!builderPublishes` branches throughout are dead too.

---

## Fix plan

### Must-fix (deploys can't succeed reliably)

| # | Status | What | Where |
|---|--------|------|-------|
| 1 | ✅ | Sandbox + events + Knative-children RBAC | `deploy/helm/vibed/templates/rbac.yaml` |
| 2 | ✅ | Workload securityContext on every deploy backend | `internal/deployer/{knative,kubernetes,sandbox}.go`, `internal/builder/buildah.go`, helpers in `internal/deployer/helpers.go` |
| 3 | ✅ | Fix AsyncDeploy double-create (pass pre-created ID into doDeploy) | `internal/orchestrator/orchestrator.go` |
| 4 | ✅ | `Delete` with empty Target attempts all known targets | `internal/orchestrator/orchestrator.go`, `internal/deployer/factory.go` (added `All()`) |
| 5 | ✅ | Extend GC to Knative Services and Sandboxes | `internal/gc/collector.go`, call site `cmd/vibed/main.go:243-245` (clients now passed in) |
| 6 | ✅ | Stop using public `nginx:alpine` for static path | `internal/orchestrator/orchestrator.go` — switched to `nginxinc/nginx-unprivileged:alpine` (non-root UID 101, /tmp writable, listens 8080). Compatible with the new runAsNonRoot policy. Followup: bake a registry-local image to remove DockerHub pull dependency. |
| 7 | ✅ | Drop `context.Background()` in orchestrator goroutines | `internal/orchestrator/orchestrator.go` (`SetLifecycleContext`), `cmd/vibed/main.go` (wires SIGTERM ctx into orch + GC) |

### Should-fix (deploys succeed but flaky/slow)

| # | Status | What | Where |
|---|--------|------|-------|
| 8 | ✅ | Configurable `waitForReady` timeout (default 10m) | `internal/config/config.go` (`Deployment.ReadyTimeout`), `internal/deployer/{knative,kubernetes}.go`, chart `values.yaml` + `configmap.yaml` |
| 9 | ✅ | Always set `containerPort` (default 8080 if unset) | `internal/deployer/knative.go` Deploy + Update |
| 10 | ✅ | Buildah Job hardening (BackoffLimit:2, ActiveDeadline, TTL:600) | `internal/builder/buildah.go` (also fixed waitForJob to honor parent ctx) |
| 11 | ✅ | Capture Buildah-pushed digest, use `image@sha256:...` | `internal/builder/{builder,buildah}.go` (`Digest` in BuildResult), `internal/orchestrator/orchestrator.go` (`pinnedImageRef`) |
| 12 | ✅ | Enrich `failArtifact` payload (Conditions, events, Job tail) | `internal/orchestrator/orchestrator.go` (`collectDeployDiagnostics` — appends recent Warning Events + pod waiting/terminated reasons to artifact.Error) |
| 13 | ✅ | Panic recovery in goroutines | `orchestrator.go` AsyncDeploy + AsyncUpdate (defer recover → failArtifact) |
| 14 | ✅ | Inject `vibed.dev/trace-id` label/annotation on every K8s object | `internal/deployer/helpers.go` (`VibedLabels`/`TraceIDLabel`), all 3 deployers + buildah Job + static ConfigMap |
| 15 | ✅ | Expose GC config in `values.yaml` + template | `deploy/helm/vibed/values.yaml`, `templates/configmap.yaml` |
| 16 | ✅ | Singleflight per artifact name | `internal/orchestrator/orchestrator.go` (`deployFlight`) |
| 17 | ✅ | Patch instead of replace in Sandbox.Update + Knative static-Update | `internal/deployer/sandbox.go` (Merge Patch), `internal/deployer/knative.go` (in-place spec update with conflict-retry, no delete-then-recreate) |

### Nice-to-have

| # | Status | What |
|---|--------|------|
| 18 | ☐ | Knative autoscaling annotations injection (`min-scale: "1"`, `initial-scale: "1"`) |
| 19 | ☐ | Idempotency keys on `deploy_artifact` |
| 20 | ☐ | Worker-pool concurrency cap (`cfg.Deployment.MaxConcurrent`, default 5) |
| 21 | ☐ | Detector cache (30s TTL) |
| 22 | ☐ | Purge dead Pack code paths (`pushImage`, `!builderPublishes`, paketobuildpacks defaults) |
| 23 | ☐ | `get_artifact_logs` streaming active build Job pod logs while `Status==building` |
| 24 | ☐ | State-machine guard in `updateStatus` — reject backwards transitions |
| 25 | ☐ | Background status reconciler — periodically re-fetch live status, update store |
| 26 | ☐ | Validate secrets in `artifact.Namespace` (post-resolution), not `cfg.Deployment.Namespace` |

---

## Working notes

Implementation order: 1 → 2 → 3 (independent, fast wins) → 4 → 5 → 6 → 7. Then should-fix list. Update the Status column in the tables above as items land. Add post-mortem notes here when surprises happen during implementation.

### Implementation log

**2026-05-08:** All 7 must-fix and all 10 should-fix items committed and deployed to the live `vibed-dev` cluster. New artifact-store ServiceAccount permissions verified live (configmaps/sandboxes/events writable). vibeD pod logs show GC running at the new 5m/1h cadence. Pod is `1/1 Running` and `/healthz` returns 200.

Surprises:
- Helm `--reuse-values` does not merge new `values.yaml` defaults into a previous release's stored values, so adding the `gc:` and `deployment.readyTimeout` blocks broke upgrades until the configmap template was wrapped in `{{- with .Values.config.gc }}`. Fresh installs were unaffected.
- Bitnami removed versioned `bitnami/kubectl:X.Y` tags from free Docker Hub in 2025; testbed charts use `bitnami/kubectl:latest`.
- The fundamental helm-CRD ordering issue (helm validates rendered manifests at install time, but `KnativeServing`'s CRD is only registered by the operator's pre-install hook) forced moving the `KnativeServing` CR creation OUT of helm templates and INTO the install Job's `kubectl apply` — see `testbed/knative/templates/operator-install-job.yaml`.
