# agent-sandbox Helm Chart (testbed)

Wraps the [`kubernetes-sigs/agent-sandbox`](https://github.com/kubernetes-sigs/agent-sandbox)
release manifests in a Helm chart. A pre-install hook Job applies the upstream
`sandbox.yaml` (controller + `Sandbox` CRD) and, optionally, `extensions.yaml`
(`SandboxTemplate`, `SandboxClaim`, `SandboxWarmPool`).

Default version: **v0.5.2**.

## Install

```sh
helm install agent-sandbox testbed/agent-sandbox/ \
  --namespace agent-sandbox-system --create-namespace
```

Pin to a different release:

```sh
helm install agent-sandbox testbed/agent-sandbox/ \
  --namespace agent-sandbox-system --create-namespace \
  --set version=v0.5.2 \
  --set manifests.core=https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.2/sandbox.yaml \
  --set manifests.extensions.url=https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.2/extensions.yaml
```

Install without the extensions CRDs:

```sh
helm install agent-sandbox testbed/agent-sandbox/ \
  --namespace agent-sandbox-system --create-namespace \
  --set manifests.extensions.enabled=false
```

## Uninstall

```sh
helm uninstall agent-sandbox -n agent-sandbox-system
# Helm only owns the install Job and its RBAC. Remove the upstream resources:
kubectl delete -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.2/extensions.yaml
kubectl delete -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.2/sandbox.yaml
```

## Caveats

- The install Job runs with a `cluster-admin` `ClusterRoleBinding` so it can apply CRDs
  and cluster-scoped RBAC. Acceptable for a local kind testbed, not for production.
- Helm does not track the resources installed by `kubectl apply`. Re-installing the chart
  with a new `version` re-applies the new manifests (additive); deleted objects in newer
  releases are not garbage-collected.
- Since v0.5.0 the CRDs use a **conversion webhook** (`v1alpha1` ⇆ `v1beta1`) served by the
  `agent-sandbox-controller` itself — it self-signs and injects the `caBundle`, so there's still
  **no cert-manager dependency**. The `waitForReady` gate matters more now: the API can't convert
  objects until that controller is `Available`.
