# Knative Helm Chart (testbed)

Operator-based Knative Serving install. A pre-install Helm hook applies the upstream
Knative Operator manifest, then a `KnativeServing` CR is created with the values you
configure here. Defaults match the existing `make install-knative` setup
(v1.17.0, Kourier on NodePort 31080).

## Install

```sh
helm install knative testbed/knative/ \
  --namespace knative-system --create-namespace
```

Override the version or ingress:

```sh
helm install knative testbed/knative/ \
  --namespace knative-system --create-namespace \
  --set knativeVersion=1.18.0 \
  --set operator.manifestURL=https://github.com/knative/operator/releases/download/knative-v1.18.0/operator.yaml \
  --set serving.ingress.kourier.httpNodePort=31080
```

## Uninstall

```sh
helm uninstall knative -n knative-system
# The chart does not own the operator or CRDs. Remove them with:
kubectl delete -f https://github.com/knative/operator/releases/download/knative-v1.17.0/operator.yaml
```

## Caveats

- The install Job runs with a `cluster-admin` `ClusterRoleBinding` so it can apply the
  operator manifest (CRDs + cluster-scoped RBAC). Acceptable for a local kind testbed,
  not for production. Use the upstream operator chart or a pre-applied operator for
  hardened environments.
- Helm does not track the operator's resources, only the `KnativeServing` CR. Upgrading
  `operator.manifestURL` re-runs the apply (additive); downgrades are not handled.
- `serving.ingress.kourier.httpNodePort` only takes effect with `serviceType=NodePort`.
