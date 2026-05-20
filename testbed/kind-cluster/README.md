# kind-cluster (testbed)

Bootstraps the kind cluster the rest of the testbed runs on. Not a Helm chart —
just a kind config file plus thin shell wrappers around `kind create cluster` /
`kind delete cluster`.

## Bring up

```sh
./bootstrap.sh                    # default: cluster=vibed-dev, runtime=podman
./bootstrap.sh --cluster mytest --runtime docker
```

## Tear down

```sh
./teardown.sh
./teardown.sh --cluster mytest --runtime docker
```

## What's in `kind-config.yaml`

- `containerdConfigPatches` enabling `config_path = /etc/containerd/certs.d`,
  required by `testbed/kind-registry/scripts/configure-containerd.sh`
- `extraPortMappings` on the control-plane node so the host can reach common
  testbed services without `kubectl port-forward`:

  | Host port | Node port | Used by |
  |-----------|-----------|---------|
  | 80        | 31080     | Caddy ingress for deployed apps (vibed-router) |
  | 443       | 31443     | Caddy TLS |
  | 3000      | 31300     | Grafana (`testbed/observability`) |
  | 9090      | 31900     | Prometheus (`testbed/observability`) |
  | 8080      | 31808     | vibeD API + Dashboard |
  | 31888     | 31888     | Keycloak (`testbed/keycloak`) — same port both sides so the issuer URL `http://localhost:31888` resolves identically from host and pod |

## Full testbed bring-up

After bootstrap, install the layered charts top-to-bottom:

```sh
./bootstrap.sh
helm install kind-registry  testbed/kind-registry/
testbed/kind-registry/scripts/configure-containerd.sh --cluster vibed-dev --runtime podman
helm install observability  testbed/observability/  -n monitoring            --create-namespace
helm install keycloak       testbed/keycloak/       -n vibed-system          --create-namespace
helm install agent-sandbox  testbed/agent-sandbox/  -n agent-sandbox-system  --create-namespace
```

Then install vibeD itself:

```sh
helm install vibed deploy/helm/vibed/ -n vibed-system \
  --set image.repository=localhost/vibed --set image.tag=dev --set image.pullPolicy=Never \
  --set config.tracing.endpoint=observability-opentelemetry-collector.monitoring:4317
```
