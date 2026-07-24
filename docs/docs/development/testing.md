---
sidebar_position: 1
---

# Testing Guide

This document maps every vibeD feature to its test coverage, whether tests are automated or manual, and what gaps remain. Use it to plan test automation and verify features during development.

## Running Tests

```bash
# Unit tests (no cluster required)
make test

# Integration tests (requires Kind cluster with vibeD deployed)
make test-integration-setup   # Load test images into Kind
make test-integration          # Full suite, ~10 min timeout
make test-integration-short    # Short mode, ~5 min timeout

# Cleanup test namespaces
make test-cleanup
```

CI runs `go test ./... -short -count=1` on every PR and push to main.

## Test Coverage Matrix

### Fully Automated (Unit Tests)

These run without a cluster and are included in CI.

| Feature | Test File | Tests | What's Covered |
|---------|-----------|-------|----------------|
| **Event Bus** | `internal/events/bus_test.go` | 8 | Pub/sub, multiple subscribers, unsubscribe, context cancel, slow consumers, concurrency |
| **Garbage Collector** | `internal/gc/collector_test.go` | 9 | Orphaned jobs/configmaps/deployments, active artifacts preserved, dry-run, context cancel |
| **SQLite Store** | `internal/store/sqlite_test.go` | 12 | CRUD, list with filters, shared_with, versions, persistence across reopens |
| **App Spec** | `internal/appspec/appspec_test.go` | 4 | Language detection, entrypoint resolution, run-command derivation |
| **Pre-baked Manifests** | `internal/prebaked/{prebaked,gate}_test.go` | 8 | Manifest parse, normalized lookup, requirements.txt + package.json gate |
| **Runner Agent** | `internal/runneragent/{agent,client,ringbuffer}_test.go` | ~12 | Inject / status / logs / stop, bearer-token auth, path-traversal rejection, log ring-buffer eviction, end-to-end client ↔ agent |
| **Runner Pool** | `internal/pool/{pool,runner}_test.go` | ~12 | Claim warm/cold, Release recycle, replenish, sweep eviction, drain, resource spec rendering |
| **Runner Deployer** | `internal/deployer/runner_test.go` | 7 | Deploy / Update / Delete / GetLogs, inject failure releases runner, crashed-process detection, language gate |
| **Preview Proxy** | `internal/preview/handler_test.go` | 6 | Path parsing + redirect, ownership-scoped 404, non-runner rejection, header stripping, X-Forwarded-* hints, upstream 502 |

### Fully Automated (Integration Tests)

These require a Kind cluster (`make dev` or `make test-integration-setup`). Tagged with `//go:build integration`.

| Feature | Test File | Tests | What's Covered |
|---------|-----------|-------|----------------|
| **K8s Deployer** | `internal/deployer/kubernetes_integration_test.go` | 7 | Deploy, update, delete, logs, URL, full lifecycle |
| **HTTP API** | `internal/frontend/handler_integration_test.go` | 3 | List apps, 404 for missing app |
| **Authentication** | `internal/auth/auth_integration_test.go` | 8 | Valid/invalid keys, missing token, skip paths, env var keys |
| **Health Checks** | `internal/health/health_integration_test.go` | 7 | Liveness, readiness, component details, not-ready state |
| **Environment Detection** | `internal/environment/detector_integration_test.go` | 5 | K8s environment detection, target selection |
| **ConfigMap Store** | `internal/store/configmap_integration_test.go` | 9 | CRUD, list, filter, duplicates (K8s-backed store) |
| **HTTP Metrics** | `internal/metrics/middleware_integration_test.go` | 2 | Request recording, path normalization |

### Not Yet Automated — Manual Test Procedures

The following features were implemented in v0.1.2 and need manual verification or future test automation.

---

#### OpenTelemetry Tracing

**Config:** `tracing.enabled: true`

| # | Test | Steps | Expected |
|---|------|-------|----------|
| 1 | Stdout traces (dev mode) | Set `tracing.enabled: true` with no endpoint. Deploy an artifact. | Trace JSON printed to stderr with a root span for the `/v1` deploy request and its nested deploy-path spans |
| 2 | OTLP export | Set `tracing.endpoint: "http://jaeger:4317"`. Deploy an artifact. Open Jaeger UI. | Trace visible with `vibed` service name and child spans |
| 3 | Sample rate | Set `tracing.sampleRate: 0.0`. Deploy 10 artifacts. | No traces emitted |
| 4 | Disabled (zero overhead) | Set `tracing.enabled: false`. Deploy an artifact. | No trace output, no performance impact |
| 5 | HTTP trace propagation | Send request with `traceparent` header. | Response includes trace context, spans nested under parent |

**Automation opportunity:** Unit test that creates a `TracerProvider` with an in-memory exporter, runs a mock deploy, and asserts span names/attributes.

---

#### Public Share Links

**Requires:** SQLite store backend, authentication enabled

| # | Test | Steps | Expected |
|---|------|-------|----------|
| 1 | Create link | `POST /v1/apps/{id}/share-links` with `{"password": "test", "expires_in": "24h"}` | 201 with token, has_password: true, expires_at set |
| 2 | Access without password | `GET /api/share/{token}` (password-protected link) | 401 "password required" |
| 3 | Access with correct password | `POST /api/share/{token}` with `{"password": "test"}` | 200 with artifact name, status, URL |
| 4 | Access with wrong password | `POST /api/share/{token}` with `{"password": "wrong"}` | 401 "password required" |
| 5 | Access no-password link | Create link without password. `GET /api/share/{token}` | 200 with artifact info |
| 6 | Expired link | Create link with `expires_in: "1s"`. Wait 2s. `GET /api/share/{token}` | 404 |
| 7 | Revoked link | Create link. `DELETE /api/share-links/{token}`. `GET /api/share/{token}` | 404 |
| 8 | List links | `GET /v1/apps/{id}/share-links` | Array of share links |
| 9 | MCP create_share_link | Call via MCP tool | Returns token and share link metadata |
| 10 | Auth bypass | `GET /api/share/{token}` without Authorization header (auth enabled) | 200 (share paths skip auth) |

**Automation opportunity:** Unit test with SQLite store + mock deploy service. Test the full create/resolve/revoke cycle. Add to `sqlite_test.go` for store-level CRUD.

---

#### Rate Limiting

**Config:** `server.rateLimit.enabled: true`

| # | Test | Steps | Expected |
|---|------|-------|----------|
| 1 | Under limit | Send 5 requests to `/v1/apps` with `requestsPerSecond: 10, burst: 20` | All return 200 |
| 2 | Exceed burst | Send 25 rapid requests with `burst: 20` | First 20 return 200, rest return 429 |
| 3 | Retry-After header | Trigger 429 | Response includes `Retry-After: 1` header |
| 4 | Per-client isolation | Two different IPs exceed limit independently | Each has its own bucket |
| 5 | Auth user keying | Two authenticated users, one exceeds limit | Only the exceeded user gets 429 |
| 6 | Skip non-API paths | Exceed limit, then `GET /healthz` | Health endpoint returns 200 (not rate-limited) |
| 7 | Metric incremented | Trigger 429. `GET /metrics` | `vibed_http_rate_limited_total` counter > 0 |

**Automation opportunity:** Unit test with `httptest.Server`, configurable rate limiter, and rapid request loop. Straightforward to automate.

```bash
# Quick manual verification:
for i in $(seq 1 25); do
  curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/v1/apps
done
```

---

#### Pagination (list_artifacts)

| # | Test | Steps | Expected |
|---|------|-------|----------|
| 1 | Default pagination | `GET /v1/apps` (no params) | Returns all apps in `items` with a `total` count |
| 2 | Custom limit | `GET /v1/apps?limit=2` | Returns exactly 2 items, `total` shows the full count |
| 3 | Offset | `GET /v1/apps?offset=1&limit=1` | Skips first app, returns second |
| 4 | Limit exceeds set | `GET /v1/apps?limit=500` | Returns all apps (no clamp); never more than exist |
| 5 | MCP pagination | `list_artifacts` with `limit: 2, offset: 0` | Response includes `total`, `offset`, `limit` fields |
| 6 | Empty result | `GET /v1/apps?offset=9999` | Returns empty `items` array, `total` still correct |

**Automation opportunity:** Add to `sqlite_test.go` — create N artifacts, verify List with various offset/limit combos. Add to `handler_integration_test.go` for REST API pagination.

---

#### Secret References

| # | Test | Steps | Expected |
|---|------|-------|----------|
| 1 | Deploy with secret ref | Create K8s Secret. Deploy with `secret_refs: {"DB_PASS": "my-secret:password"}` | Container receives `DB_PASS` env var from Secret |
| 2 | Missing secret | Deploy with ref to non-existent Secret | Error: "secret not found" |
| 3 | Invalid format | Deploy with `secret_refs: {"X": "invalid"}` | Error: "must be in format 'secret-name:key'" |
| 4 | Store round-trip | Deploy with secret refs. `get_artifact_status` | SecretRefs shown in status, actual values never stored |

**Automation opportunity:** Integration test that creates a K8s Secret, deploys with secret_refs, and verifies the container env.

---

#### OIDC Authentication

| # | Test | Steps | Expected |
|---|------|-------|----------|
| 1 | Valid JWT | Configure OIDC issuer. Send request with valid JWT. | 200, user identity extracted from claims |
| 2 | Expired JWT | Send expired token | 401 |
| 3 | Wrong audience | Send JWT with different audience | 401 |
| 4 | Admin role mapping | JWT with `roleClaim` containing `adminRole` value | User gets admin privileges |
| 5 | Metadata endpoint | `GET /.well-known/oauth-protected-resource` | Returns JSON with authorization_servers |

**Automation opportunity:** Use a test JWT library to generate tokens with a mock JWKS endpoint. Verify middleware accepts/rejects correctly.

---

## Test Utilities

The `tests/testutil/` package provides helpers for integration tests:

| Helper | Description |
|--------|-------------|
| `SkipIfNoCluster(t)` | Skip test if no K8s cluster available |
| `MustGetClients(t)` | Create K8s clients (fatal on failure) |
| `CreateTestNamespace(t, clientset)` | Create isolated namespace with auto-cleanup |
| `WaitForDeploymentReady(...)` | Poll until deployment is ready |
| `WaitForPodRunning(...)` | Poll until pod is running |
| `TestConfig(ns, tmpDir)` | Generate test config with in-memory store |
| `RandomName()` | Generate DNS-safe random artifact names |

## Priority Automation Roadmap

Based on risk and effort, here's the recommended order for automating manual tests:

1. **Rate Limiting** (low effort, high value) — Unit test with `httptest`, no cluster needed
2. **Pagination** (low effort) — Extend existing `sqlite_test.go` and `handler_integration_test.go`
3. **Share Links** (medium effort) — SQLite CRUD unit tests + REST handler integration tests
4. **Tracing** (medium effort) — In-memory exporter, assert span names/attributes
5. **Secret References** (medium effort) — Integration test with K8s Secret
6. **OIDC Auth** (high effort) — Requires mock JWKS server
