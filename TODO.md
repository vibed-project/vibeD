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

- [ ] **#7 Silent store update failures**
  Six places in `orchestrator.go` use `_ = o.store.Update(ctx, artifact)`. If the ConfigMap store fails, artifact state becomes inconsistent.
  _Fix: Log store update errors with `slog.Warn`._

- [ ] **#8 No max file size / max lines validation**
  MCP tools accept arbitrarily large file maps (memory exhaustion) and unlimited log line requests.
  _Fix: Cap at 50MB total files, 10,000 log lines._

- [ ] **#9 Duplicated code across deployers**
  `buildEnvVars()`, static file volume mounting, and `resolveURL()` are copy-pasted between `knative.go` and `kubernetes.go`.
  _Fix: Extract to shared helpers in `deployer/helpers.go`._

- [ ] **#10 RBAC too permissive for pods**
  ClusterRole grants `create`, `update`, `delete` on pods. vibeD only needs `get`, `list`, `watch` (it creates Jobs, not bare pods).
  _Fix: Split RBAC rules into logical groups._

- [ ] **#11 Frontend missing delete button**
  Backend supports `delete_artifact` but the dashboard has no way to delete artifacts. Also missing: search/filter, copy URL.

- [ ] **#12 API client has no timeout**
  `fetch()` calls in `api/client.ts` can hang indefinitely.
  _Fix: Add `AbortController` with 30-second timeout._

- [ ] **#13 Helm defaults not production-safe**
  - `image.tag: "latest"` with `pullPolicy: IfNotPresent` = stale images
  - `replicaCount: 1` = no HA
  - `auth.enabled: false` = no security
  - No `securityContext` (pod runs as root)
  _Fix: Add comments, set safer defaults, add security context._

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
