---
sidebar_position: 3
---

# Monitoring and Observability

vibeD exposes Prometheus metrics, health endpoints, and structured logs for production observability.

## Prometheus Metrics

vibeD exposes metrics at `/metrics` on port 8080. This endpoint is always open (no authentication required) to allow Prometheus scraping without credential management.

When `metrics.enabled: true` (the default), the Helm chart adds standard Prometheus annotations to the pod:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "8080"
prometheus.io/path: "/metrics"
```

Most Prometheus installations with annotation-based discovery will scrape vibeD automatically.

## Available Metrics

### Deployment Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_deploys_total` | Counter | `status`, `target` | Total deployments |
| `vibed_deploy_duration_seconds` | Histogram | `status`, `target` | Deploy duration (buckets: 1s, 2s, 5s, 10s, 30s, 60s) |

### Artifact Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_artifacts_active` | Gauge | `target` | Currently active artifacts by deployment target |
| `vibed_deletes_total` | Counter | `status` | Total artifact deletions |

### MCP Tool Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_mcp_tool_calls_total` | Counter | `tool`, `status` | MCP tool invocations |
| `vibed_mcp_tool_call_duration_seconds` | Histogram | `tool` | MCP tool call duration (default Prometheus buckets) |

### Garbage Collector Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_gc_resources_cleaned_total` | Counter | `type` | Total resources cleaned by garbage collector |

The `type` label values are: `job`, `configmap`, `deployment`, `service`,
and `sandbox`.

The GC runs periodically (default: every 1 hour) and removes orphaned Kubernetes resources whose artifact no longer exists in the store. See [Configuration Reference](../configuration/config-reference.md) for GC settings.

### Warm Pool Metrics

Emitted by the [warm pools](../concepts/lanes-and-templates.md) that back the deploy path.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_pool_runners_idle` | Gauge | `language` | Warm idle runner pods available to claim |
| `vibed_pool_claims_total` | Counter | `language`, `source` | Runner claims, by `source` (`warm` pool hit vs `cold` on-demand) |
| `vibed_pool_claim_duration_seconds` | Histogram | `language`, `source` | Time to obtain a runner |
| `vibed_pool_runners_created_total` | Counter | `language`, `status` | Runner pods created, by outcome (`ready` vs `failed` warm-up) |

A healthy pool keeps `vibed_pool_runners_idle` at the configured pool size and
serves most claims from the `warm` source; a rising `cold` claim rate means the
pool is being drained faster than it replenishes.

### HTTP API Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_http_requests_total` | Counter | `method`, `path`, `status_code` | HTTP API requests |
| `vibed_http_request_duration_seconds` | Histogram | `method`, `path` | HTTP request duration (default Prometheus buckets) |

HTTP paths are normalized to prevent high cardinality (e.g., `/api/artifacts/:id` instead of individual artifact IDs).

### SSE Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_sse_connections_active` | Gauge | - | Number of active Server-Sent Events connections |

The SSE endpoint (`GET /api/events`) streams real-time artifact lifecycle events to connected dashboard clients. This gauge tracks how many clients are currently connected.

### Rate Limiting Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_http_rate_limited_total` | Counter | `client_type` | HTTP requests rejected by rate limiting |

The `client_type` label is `apikey` when the client is authenticated or `ip` when identified by IP address. See [Configuration Reference](../configuration/config-reference.md) for rate limit settings.

### Governance Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_quota_rejections_total` | Counter | `scope` | Deploys rejected by [quota](../configuration/quotas.md), by the ceiling that tripped (`owner` or `department`) |
| `vibed_audit_events_total` | Counter | `action`, `outcome` | [Audit](../configuration/audit-log.md) events recorded, by action (`deploy`/`delete`/`rollback`) and outcome (`ok`/`denied`/`error`) |

These are served on the main server's `/metrics` (:8080). The bring-your-own base-image validator emits one more, but on the **controller's** metrics endpoint (:8081):

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_template_validation` | Gauge | `template`, `result` | Per-slot [base-image validation](../configuration/custom-base-images.md) state; alert on `vibed_template_validation{result="invalid"} == 1` |

### Label Values

| Label | Possible Values |
|-------|----------------|
| `status` | `success`, `error` |
| `language` | `nodejs`, `python`, `go`, `static` |
| `target` | `sandbox`, `kubernetes` |
| `tool` | `deploy_artifact`, `update_artifact`, `list_artifacts`, `get_artifact_status`, `get_artifact_logs`, `delete_artifact`, `list_deployment_targets` |

## Scraping with Prometheus

### Annotation-Based Discovery (Default)

If you use kube-prometheus-stack or a similar Prometheus Operator setup with annotation-based pod discovery, vibeD is scraped automatically. No additional configuration is needed.

### ServiceMonitor (Prometheus Operator)

For explicit scrape configuration with the Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: vibed
  namespace: vibed-system
  labels:
    release: prometheus    # Must match your Prometheus Operator's selector
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: vibed
  endpoints:
    - port: http
      path: /metrics
      interval: 30s
```

### PodMonitor (Alternative)

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: vibed
  namespace: vibed-system
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: vibed
  podMetricsEndpoints:
    - port: http
      path: /metrics
      interval: 30s
```

## Example Alert Rules

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: vibed-alerts
  namespace: vibed-system
spec:
  groups:
    - name: vibed.rules
      rules:
        - alert: VibeDHighDeployFailureRate
          expr: rate(vibed_deploys_total{status="error"}[5m]) > 0.1
          for: 10m
          labels:
            severity: critical
          annotations:
            summary: "Deploy failure rate is elevated"

        - alert: VibeDHighArtifactCount
          expr: sum(vibed_artifacts_active) > 100
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "High number of active artifacts ({{ $value }})"

        - alert: VibeDGCHighCleanupRate
          expr: rate(vibed_gc_resources_cleaned_total[1h]) > 10
          for: 30m
          labels:
            severity: warning
          annotations:
            summary: "GC is cleaning many orphaned resources ({{ $value }}/hr)"
```

## Health Endpoints

vibeD exposes two health endpoints that are always open (no authentication required):

| Endpoint | Purpose | Used By |
|----------|---------|---------|
| `/healthz` | Liveness probe | Kubernetes restarts the pod if this fails |
| `/readyz` | Readiness probe | Kubernetes removes the pod from service if this fails |

Both return JSON responses:

```json
// GET /healthz
{
  "status": "ok",
  "uptime": "2h30m15s"
}

// GET /readyz
{
  "status": "ready",
  "components": {
    "store": "ok",
    "kubernetes": "ok"
  }
}
```

The Helm chart configures these probes with sensible defaults:

| Probe | Initial Delay | Period | Timeout |
|-------|--------------|--------|---------|
| Liveness (`/healthz`) | 5s | 30s | 3s |
| Readiness (`/readyz`) | 3s | 10s | 3s |

## Grafana Dashboard

The `testbed/observability` stack ships a ready-made **vibeD Overview** dashboard
(`testbed/observability/dashboards/vibed-overview.json`) — installed
automatically by `make install-observability`. You can also build your own from
the metrics above. Recommended panels:

- **Deploy Success Rate** - `rate(vibed_deploys_total{status="success"}[5m]) / rate(vibed_deploys_total[5m])`
- **Active Artifacts** - `sum(vibed_artifacts_active)` by target
- **MCP Tool Usage** - `rate(vibed_mcp_tool_calls_total[5m])` by tool
- **HTTP Request Rate** - `rate(vibed_http_requests_total[5m])` by status_code
- **HTTP Latency P99** - `histogram_quantile(0.99, rate(vibed_http_request_duration_seconds_bucket[5m]))`
- **GC Cleanup Rate** - `rate(vibed_gc_resources_cleaned_total[1h])` by type
- **SSE Connections** - `vibed_sse_connections_active`
- **Idle Runners** - `vibed_pool_runners_idle` by language
- **Runner Claims (warm vs cold)** - `rate(vibed_pool_claims_total[5m])` by source
- **Claim Latency P99** - `histogram_quantile(0.99, sum(rate(vibed_pool_claim_duration_seconds_bucket[5m])) by (le))`

## Distributed Tracing (OpenTelemetry)

vibeD supports OpenTelemetry distributed tracing, providing end-to-end visibility into the deploy pipeline. Each deploy produces a trace with child spans for build, push, and deploy steps.

### Enabling Tracing

```yaml
tracing:
  enabled: true
  endpoint: "http://jaeger:4317"   # OTLP gRPC endpoint
  sampleRate: 1.0                  # 1.0 = sample all, 0.1 = 10%
```

Or via environment variables:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4317   # Also enables tracing
export VIBED_TRACING_SAMPLE_RATE=1.0
```

### Exporters

| Configuration | Behavior |
|---------------|----------|
| `endpoint` set | Sends traces via OTLP gRPC to the specified collector (Jaeger, Tempo, etc.) |
| `endpoint` empty | Prints traces to stdout in pretty-print format (development mode) |
| `enabled: false` | No-op tracer, zero overhead |

### Trace Structure

A deploy operation produces spans like:

```
orchestrator.Deploy (root)
  +-- builder.Build
  +-- deployer.Deploy
```

Update and rollback operations are similarly instrumented. HTTP requests are traced via the `otelhttp` middleware, which extracts and injects `traceparent` headers.

### Viewing Traces

Any OpenTelemetry-compatible backend works: Jaeger, Grafana Tempo, Datadog, Honeycomb, or New Relic. For the dev setup the `testbed/observability` chart bundles Tempo (plus Loki for logs and Prometheus for metrics); use the stdout exporter for quick debugging without a backend.
