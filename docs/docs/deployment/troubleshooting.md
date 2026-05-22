---
sidebar_position: 4
---

# Troubleshooting

Common issues, mostly drawn from real cluster bring-up.

## A new status field is always empty on-cluster

You changed the `VibedApp` CRD and `helm upgrade`d, but a new `status.*` field never persists.

**Cause:** Helm installs CRDs from `crds/` only on *first install* and never on upgrade, so the live CRD lacks the new field and the apiserver silently strips it from writes.

**Fix:** apply the CRD by hand.

```bash
kubectl apply -f deploy/helm/vibed/crds/vibed.dev_vibedapps.yaml
# verify the field is in the schema:
kubectl get crd vibedapps.vibed.dev -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.status.properties.<field>}'
```

## App stuck in `Starting`

```bash
kubectl get vibedapp <name> -n vibed-apps -o jsonpath='{range .status.conditions[*]}{.type}={.reason}: {.message}{"\n"}{end}'
```

- **`InjectFailed … context deadline exceeded`** — the sandbox agent couldn't pull the source tarball. The usual cause in dev is a stale `storage.tarball.served.publicBaseURL` (e.g. a hardcoded Service ClusterIP that changed on reinstall). Leave it empty so it defaults to the Service DNS name. In production, this means `served` is being used where `s3` is required — switch to `s3`.
- **`AgentUnreachable`** — the controller can't reach the sandbox agent on `:9000`. Check the sandbox NetworkPolicy: `Managed` mode blocks the control plane. Use `runtime.sandboxNetworkPolicy: Unmanaged` + `networkPolicy.enabled: true`.

## App is `Ready` but the URL 502s / isn't reachable

- **502 after a sandbox pod restart** — the route follows a per-app `Service`, and the controller re-injects source on pod recreation, so this self-heals within a reconcile. If it persists, check the Service has endpoints: `kubectl get endpoints vibed-app-<label> -n vibed-apps`.
- **`https://<label>.localhost` won't open (dev)** — dev Caddy serves plain HTTP behind a port-forward. Use the URL vibeD actually returns: `http://<label>.localhost:18080`. `*.localhost` resolves in Chrome/Firefox; in Safari add a `/etc/hosts` entry or use Chrome.
- **Caddy route missing** — check `vibed-router` logs and the Caddy admin API:

  ```bash
  kubectl exec -n vibed-system deploy/vibed-caddy -- wget -qO- http://localhost:2019/config/apps/http/servers
  ```

## App deployed but not in the dashboard

The dashboard lists apps via `/v1/apps`. Confirm the API returns it:

```bash
curl http://localhost:18090/v1/apps
```

If it's there but not in the UI, hard-refresh the browser to drop a stale cached bundle. Note that auth scopes the list to the caller's `owner`; with auth disabled everything is owned by `admin`.

## Warm pool empty / claims time out

```bash
kubectl get sandboxwarmpool -n vibed-apps
kubectl get pods -n vibed-apps
```

- Template image not pullable → warm sandboxes never become Ready. Check the `warmPools.<template>.image` ref and registry credentials.
- agent-sandbox controller not running → no binding happens. Confirm it's installed and healthy.

## Diagnostic commands

| Command | Purpose |
|---|---|
| `kubectl get pods -n vibed-system` | Control-plane status |
| `kubectl logs -n vibed-system deploy/vibed-controller` | Reconcile / claim / inject logs |
| `kubectl logs -n vibed-system deploy/vibed-router` | Route programming |
| `kubectl get vibedapp -A` | All apps + phase + URL |
| `curl -s localhost:18090/metrics \| grep vibed_` | Prometheus metrics |
