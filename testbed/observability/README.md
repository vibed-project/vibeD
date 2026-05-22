# Observability Helm Chart (testbed)

LGTM-ish stack as an umbrella chart: **Prometheus**, **Grafana**, **Tempo**, **Loki**,
**Promtail**, and the **OpenTelemetry Collector**, with a sample vibeD dashboard
shipped via Grafana's sidecar.

Relocated from `deploy/helm/vibed-observability/` so it can be reused across testbed
clusters and isn't conceptually owned by vibeD.

## Install

```sh
helm dependency build testbed/observability/
helm install observability testbed/observability/ \
  --namespace monitoring --create-namespace --wait --timeout 10m
```

> **Release name pinning** — Subchart service names are derived from the helm release
> name (e.g. `observability-prometheus-server`). The Grafana datasources, Promtail
> client URL, and OTel→Tempo exporter in `values.yaml` all assume the release is
> named `observability`. If you install under a different name, override those URLs
> on the CLI or in your own values file.

## Send traces from an app

OTLP/gRPC endpoint:

```
observability-opentelemetry-collector.monitoring.svc.cluster.local:4317
```

For vibeD specifically:

```sh
helm upgrade vibed deploy/helm/vibed/ \
  --namespace vibed-system --reuse-values \
  --set config.tracing.enabled=true \
  --set config.tracing.endpoint=observability-opentelemetry-collector.monitoring:4317
```

## Disable the bundled vibeD dashboard

```sh
helm install observability testbed/observability/ \
  -n monitoring --create-namespace \
  --set vibedDashboard.enabled=false
```

## Migrating from `vibed-observability`

```sh
helm uninstall vibed-observability -n monitoring
helm dependency build testbed/observability/
helm install observability testbed/observability/ -n monitoring --wait --timeout 10m
# Then point vibeD at the new collector hostname (see "Send traces" above).
```

## Uninstall

```sh
helm uninstall observability -n monitoring
```

PVCs created by Loki/Tempo SingleBinary mode are *not* removed by `helm uninstall` —
delete them manually if you want a clean slate:

```sh
kubectl -n monitoring delete pvc -l app.kubernetes.io/instance=observability
```
