# kind-registry Helm Chart (testbed)

A minimal in-cluster Docker registry for kind testbed clusters. Replaces
`deploy/kind/registry.yaml`, `deploy/kind/registry-external.yaml`, and the
`make setup-registry` shell glue.

The chart only owns **in-cluster** resources (Deployment, Service, optional
ExternalName aliases, optional PVC). The host-side containerd config — which
makes `kind-registry:5000` resolvable from pulls — is provided as a sibling
shell script you run once after install:

```sh
testbed/kind-registry/scripts/configure-containerd.sh \
  --cluster vibed-dev --runtime podman
```

## Install

```sh
helm install kind-registry testbed/kind-registry/
testbed/kind-registry/scripts/configure-containerd.sh \
  --cluster vibed-dev --runtime podman
```

## Customizing

```sh
helm install kind-registry testbed/kind-registry/ \
  --set persistence.enabled=true \
  --set persistence.size=10Gi \
  --set 'aliases[0].namespace=vibed-system' \
  --set 'aliases[0].createNamespace=true' \
  --set 'aliases[1].namespace=knative-serving'
```

## Prerequisites

The kind cluster must have been created with `containerdConfigPatches` enabling
the per-host config directory. See `deploy/kind/kind-config.yaml`:

```yaml
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"
```

Without that, `configure-containerd.sh` will write a `hosts.toml` that
containerd ignores.

## Why a script instead of a Job?

Patching containerd needs write access to `/etc/containerd/certs.d` on the kind
node, which sits outside the cluster's API surface. The supported way to reach
it is `<runtime> exec <kind-node>`. A privileged hostPath Job could do it from
inside the cluster, but adds escalated permissions for a one-shot configuration
step. A plain shell script is the honest fit.

## Uninstall

```sh
helm uninstall kind-registry
# Optional: remove the host-side hosts.toml
podman exec vibed-dev-control-plane rm -rf /etc/containerd/certs.d/kind-registry:5000
```
