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

### Build Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vibed_builds_total` | Counter | `status`, `language` | Total container image builds |
| `vibed_build_duration_seconds` | Histogram | `status`, `language` | Build duration (buckets: 5s, 10s, 30s, 60s, 120s, 300s, 600s) |
| `vibed_builds_in_flight` | Gauge | - | Number of builds currently in progress |

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

The `type` label values are: `job`, `configmap`, `deployment`, `service`.

The GC runs periodically (default: every 1 hour) and removes orphaned Kubernetes resources whose artifact no longer exists in the store. See [Configuration Reference](../configuration/config-reference.md) for GC settings.

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

### Label Values

| Label | Possible Values |
|-------|----------------|
| `status` | `success`, `error` |
| `language` | `nodejs`, `python`, `go`, `static` |
| `target` | `knative`, `kubernetes`, `wasmcloud` |
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
        - alert: VibeDHighConcurrentBuilds
          expr: vibed_builds_in_flight > 5
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "Too many concurrent builds ({{ $value }})"

        - alert: VibeDHighBuildFailureRate
          expr: rate(vibed_builds_total{status="error"}[5m]) > 0.1
          for: 10m
          labels:
            severity: critical
          annotations:
            summary: "Build failure rate is elevated"

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

        - alert: VibeDSlowBuilds
          expr: histogram_quantile(0.99, rate(vibed_build_duration_seconds_bucket[10m])) > 300
          for: 15m
          labels:
            severity: warning
          annotations:
            summary: "P99 build duration exceeds 5 minutes"

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

vibeD does not ship a bundled Grafana dashboard, but you can build one from the metrics above. Recommended panels:

- **Build Rate** - `rate(vibed_builds_total[5m])` by status
- **Build Duration P99** - `histogram_quantile(0.99, rate(vibed_build_duration_seconds_bucket[5m]))`
- **Concurrent Builds** - `vibed_builds_in_flight`
- **Deploy Success Rate** - `rate(vibed_deploys_total{status="success"}[5m]) / rate(vibed_deploys_total[5m])`
- **Active Artifacts** - `sum(vibed_artifacts_active)` by target
- **MCP Tool Usage** - `rate(vibed_mcp_tool_calls_total[5m])` by tool
- **HTTP Request Rate** - `rate(vibed_http_requests_total[5m])` by status_code
- **HTTP Latency P99** - `histogram_quantile(0.99, rate(vibed_http_request_duration_seconds_bucket[5m]))`
- **GC Cleanup Rate** - `rate(vibed_gc_resources_cleaned_total[1h])` by type
- **SSE Connections** - `vibed_sse_connections_active`
