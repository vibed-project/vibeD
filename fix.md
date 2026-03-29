# vibeD Security & Performance Fixes

## Security Findings

| Severity | # | Issue | File |
|----------|---|-------|------|
| **HIGH** | 1 | **No request body size limit** — any endpoint accepts unbounded JSON, enabling OOM | `handler.go` (all POST handlers) |
| **HIGH** | 2 | **SSE events leak across users** — all authenticated users see all artifact events | `sse.go` |
| **HIGH** | 3 | **Command injection surface in build commands** — `sh -c` with `fmt.Sprintf` for image names | `buildah.go:109`, `wasm.go:127` |
| MEDIUM | 4 | OIDC accepts any audience when `audience` is empty | `auth.go:208` |
| MEDIUM | 5 | OAuth passthrough trusts any token + `X-Forwarded-User` | `auth.go:125` |
| MEDIUM | 6 | EnvVars exposed in artifact detail API responses | `types.go:40` |
| MEDIUM | 7 | Error messages leak internal paths/SQL details | `handler.go` (multiple) |
| MEDIUM | 8 | No HTTP security headers (X-Frame-Options, CSP, etc.) | `main.go` |
| MEDIUM | 9 | Wasm build PVC mounted read-write (should be read-only) | `wasm.go:155` |
| MEDIUM | 10 | RBAC uses ClusterRole (too broad) | Helm `rbac.yaml` |
| MEDIUM | 11 | Share link password brute-force not rate-limited per token | `ratelimit.go` |
| MEDIUM | 12 | Artifact ID only 64-bit entropy + timestamp fallback | `orchestrator.go:757` |
| LOW | 13 | Graceful shutdown uses `Close()` not `Shutdown()` | `main.go:388` |
| LOW | 14 | Rate limiter cleanup goroutine never stops, no map size cap | `ratelimit.go:31` |
| LOW | 15 | Missing `secrets` RBAC permission for secret ref validation | Helm `rbac.yaml` |

## Performance Findings

| Impact | # | Issue | File |
|--------|---|-------|------|
| **HIGH** | 1 | **Missing SQLite indexes** on `status`, `owner_id`, `artifact_id` | `sqlite.go` schema |
| **HIGH** | 2 | **List query fetches all rows, filters ownership in Go** — should push to SQL | `sqlite.go:189` |
| **HIGH** | 3 | **GC N+1 queries** — individual `store.Get()` per orphaned resource | `collector.go:135,186,225` |
| **HIGH** | 4 | **No HTTP server timeouts** — vulnerable to slowloris | `main.go:379` |
| MEDIUM | 5 | Missing `PRAGMA synchronous=NORMAL` (2-3x slower writes with WAL) | `sqlite.go:97` |
| MEDIUM | 6 | Buildah job polling fixed 2s interval (hundreds of K8s API calls) | `buildah.go:201` |
| MEDIUM | 7 | Blocking `time.Sleep(2s)` in deploy path for stale job cleanup | `buildah.go:172` |
| MEDIUM | 8 | No SSE connection limit — DoS vector | `sse.go` |
| MEDIUM | 9 | No prepared statements for hot-path queries | `sqlite.go` throughout |
| MEDIUM | 10 | ConfigMap delete+create is non-atomic (brief outage) | `orchestrator.go:903` |
| MEDIUM | 11 | Ignored `json.Unmarshal` errors — silent data loss | `sqlite.go` throughout |
| LOW | 12 | Rate limiter uses Mutex instead of RWMutex | `ratelimit.go:28` |
| LOW | 13 | Duplicate deploy-finalize code across 5 methods | `orchestrator.go` |
| LOW | 14 | `fmt.Sprintf("%x")` slower than `hex.EncodeToString` | `orchestrator.go:763` |

## Top Priority Fixes

1. [x] Add `http.MaxBytesReader` to all HTTP handlers
2. [x] Add HTTP server timeouts
3. [x] Filter SSE events by artifact ownership
4. [x] Add SQLite indexes
5. [x] Add `PRAGMA synchronous=NORMAL`
6. [x] Use `server.Shutdown()` instead of `Close()`
7. [x] Add security headers middleware
8. [x] Increase artifact ID to 128-bit, remove timestamp fallback
