---
sidebar_position: 1
---

# Installation

vibeD ships as a Helm chart and runs on any Kubernetes 1.29+ cluster with a compatible node pool. For a laptop setup, see [Local development](local-dev.md) instead.

## Prerequisites

1. **A Kubernetes cluster** (EKS/GKE/AKS or on-prem), 1.29+.
2. **[agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) installed** (v0.4.5+). vibeD builds on its `Sandbox`, `SandboxTemplate`, `SandboxClaim`, and `SandboxWarmPool` CRDs and its controller. Install it before vibeD.
3. **A Kata RuntimeClass** for the general lane — `kata-qemu` (works without nested virt) or `kata-fc` (Firecracker, needs KVM). The general lane runs user code in Kata microVMs.
4. **A sandbox node pool** labeled `vibed.dev/sandbox-node: "true"` running `containerd` + `containerd-shim-kata-v2`. Kata + Firecracker needs KVM (bare metal, `*.metal` on AWS, or nested-virt images on GCP).
5. **Object storage** (S3 or MinIO) for source tarballs in production — see [storage](../configuration/storage.md).
6. **A DNS-01 capable DNS provider** (Cloudflare, Route53, …) for Caddy's wildcard TLS cert on `*.<your-domain>`.

## Install

```bash
helm install vibed deploy/helm/vibed/ \
  -n vibed-system --create-namespace \
  --set controller.domain=apps.example.com \
  --set config.storage.tarball.backend=s3 \
  --set config.storage.tarball.s3.bucket=vibed-sources \
  --set config.storage.tarball.s3.region=us-east-1
```

This installs the control plane (vibed, vibed-controller, vibed-router, Caddy) into `vibed-system` and the warm pools into the `vibed-apps` workloads namespace.

:::warning CRD upgrades
The `VibedApp` CRD lives in the chart's `crds/` directory. Helm installs CRDs **only on first install and never on `helm upgrade`**. After upgrading to a version that changes the CRD schema, apply it manually:

```bash
kubectl apply -f deploy/helm/vibed/crds/vibed.dev_vibedapps.yaml
```
:::

## Verify

```bash
kubectl get pods -n vibed-system            # control plane Running
kubectl get sandboxwarmpool -n vibed-apps   # warm pools populated
```

## Production essentials

- **Source backend = `s3`.** The `served` backend (vibeD serves blobs from its own PVC) only works in dev. In production, sandboxes run under a restrictive NetworkPolicy with no cluster DNS, so the agent can only pull from a pre-signed S3/MinIO URL.
- **Sandbox NetworkPolicy.** Set `runtime.sandboxNetworkPolicy: Unmanaged` and `networkPolicy.enabled: true` so vibeD owns a policy that permits exactly the control-plane → sandbox traffic plus DNS + S3 egress.
- **Pin image tags** (not `latest`) and enable auth before exposing the API.

See the [configuration reference](../configuration/config-reference.md) for every value, and the [production guide](../deployment/production-guide.md) for the full hardened setup.
