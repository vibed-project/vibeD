# vibeD v1 TODO

## Critical Bugs (Must Fix)

- [x] **#1 Concurrent deploy race — duplicate artifact names**
  Orchestrator `Deploy()` does not check if an artifact with the same name already exists before creating. Two concurrent calls can create duplicates.
  _Fix: Check `store.GetByName()` before `store.Create()`._

- [x] **#2 File path traversal not validated**
  File paths like `../../etc/passwd` in the `files` map are written directly to storage without sanitization.
  _Fix: Validate all file paths — reject `..`, absolute paths, and backslashes._

- [x] **#3 `os.ReadDir` error silently ignored in Buildah builder**
  `buildah.go`: `entries, _ := os.ReadDir(req.SourceDir)` — if this fails, the `files` map is empty and a broken Dockerfile is generated silently.
  _Fix: Return the error instead of discarding it._

- [x] **#4 Unchecked container index access in deployers**
  Both `knative.go` and `kubernetes.go` `Update()` methods access `Containers[0]` without checking slice length. Panics if the container list is empty.
  _Fix: Add bounds check before accessing `Containers[0]`._

- [x] **#5 `BuildsInFlight` metric never decremented on context cancel**
  If a build's context is cancelled between `Inc()` and `Dec()`, the gauge drifts permanently. Same issue exists for `ArtifactsActive` on delete failure paths.
  _Fix: Use `defer` for metric decrement._

- [x] **#6 Config secret resolution returns empty string on failure**
  `resolve.go`: `env:VAR_NAME` silently returns `""` if the variable is unset. Auth tokens resolving to empty cause silent failures downstream.
  _Fix: Return errors from `ResolveSecret` and propagate them at config load time._

## Important (v1 Quality)

- [x] **#7 Silent store update failures**
  Six places in `orchestrator.go` use `_ = o.store.Update(ctx, artifact)`. If the ConfigMap store fails, artifact state becomes inconsistent.
  _Fix: Log store update errors with `slog.Warn`._

- [x] **#8 No max file size / max lines validation**
  MCP tools accept arbitrarily large file maps (memory exhaustion) and unlimited log line requests.
  _Fix: Added configurable `LimitsConfig` (maxTotalFileSize: 50MB, maxFileCount: 500, maxLogLines: 10000). Enforced in deploy/update/logs MCP tools. Configurable via YAML, Helm values, and env vars._

- [x] **#9 Duplicated code across deployers**
  `buildEnvVars()`, static file volume mounting, and `GetLogs()` were copy-pasted across deployers.
  _Fix: Extracted `BuildEnvVars()`, `StaticFileVolumes()`, and `FetchPodLogs()` to `deployer/helpers.go`. All three deployers now use shared helpers._

- [x] **#10 RBAC too permissive for pods**
  ClusterRole grants `create`, `update`, `delete` on pods. vibeD only needs `get`, `list`, `watch` (it creates Jobs, not bare pods).
  _Fix: Split RBAC rules into logical groups — pods/pods-log now read-only._

- [x] **#11 Frontend missing delete button**
  Backend supports `delete_artifact` but the dashboard has no way to delete artifacts. Also missing: search/filter, copy URL.
  _Fix: Added DELETE /api/artifacts/{id} handler, deleteArtifact() API client function, delete button with inline confirmation in ArtifactCard._

- [x] **#12 API client has no timeout**
  `fetch()` calls in `api/client.ts` could hang indefinitely.
  _Fix: Added `fetchWithTimeout()` wrapper using `AbortController` with 30-second timeout. All 5 API functions now use it._

- [x] **#13 Helm defaults not production-safe**
  - `image.tag: "latest"` with `pullPolicy: IfNotPresent` = stale images
  - `replicaCount: 1` = no HA
  - `auth.enabled: false` = no security
  - No `securityContext` (pod runs as root)
  _Fix: Added podSecurityContext (runAsNonRoot, nobody user, seccomp), container securityContext (no privilege escalation, read-only rootfs, drop ALL caps), /tmp emptyDir, and documentation comments throughout values.yaml._

## Nice-to-Have (Post-v1)

- [ ] Port validation (1-65535 range check)
- [ ] Pagination for artifact list API
- [ ] Artifact versioning / rollback support
- [ ] Webhook notifications for deploy lifecycle events
- [ ] OpenAPI/Swagger documentation generation
- [ ] NetworkPolicy in Helm chart
- [ ] Rate limiting middleware
- [ ] Loading skeletons in the dashboard UI
- [ ] Log export/download from UI
- [ ] Custom domain support for Knative services
