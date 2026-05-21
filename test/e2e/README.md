# End-to-end tests

Two layers, matching what can run where.

## In-process E2E (default — `slice_test.go`)

Wires the **real** components together — the `/v1` HTTP server, the deploy
service, the classifier, the `vibed-controller` reconciler, and `vibed-router`
— against a controller-runtime fake API and an httptest fake Caddy. A
"reconcile pump" goroutine stands in for the controller-manager; agent-sandbox
binding and the vibed-agent probe are simulated by the controller's `Dummy*`
implementations.

It deploys one fixture per classifier destination (static-nginx, node-24,
python-313, go-123, base-al2023, workerd) and drives each all the way to a
programmed Caddy route, then tears it down — the closest thing to refactor.md
§10.2 ("deploy each fixture, assert a route") that runs **without a cluster,
Kata, workerd, or built images**.

```sh
make e2e          # or: go test ./test/e2e/...
```

## Cluster E2E (`cluster_test.go`, build tag `e2ecluster`)

The literal §10.2 smoke test against a **real cluster with the full stack
installed**. Gated behind the `e2ecluster` tag and skips when no cluster (or
no `VibedApp` CRD) is reachable, so it never breaks the default `go test`.

```sh
make dev          # kind + agent-sandbox + deps
helm install vibed deploy/helm/vibed -n vibed-system --create-namespace
make e2e-cluster  # go test -tags=e2ecluster ./test/e2e/...
```

It creates a `VibedApp` and waits for the controller to drive it to `Ready`
with a URL. The richer "POST /v1/deploy, curl the URL, p99 < 10s over 100
deploys" loop belongs in a load harness (`test/load/`) once the runtime images
are published — see refactor.md §10.3.
