---
sidebar_position: 3
---

# Installation

This page installs vibeD on a real Kubernetes cluster (1.29+).

:::tip Just trying it out?
For a laptop / evaluation setup, use **[Local development](local-dev.md)** — a single `make dev` stands up kind, all dependencies, and vibeD. This page is for a shared or production cluster.
:::

## Prerequisites

1. **Kubernetes 1.29+** (EKS/GKE/AKS or on-prem), with cluster-admin to install CRDs.
2. **[agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) — core *and* extensions** (v0.4.5+). vibeD renders `SandboxTemplate` and `SandboxWarmPool` objects, so the **extensions** CRDs must exist before you install vibeD. [Step 1](#step-1--install-agent-sandbox) installs them.
3. **A sandbox node pool** labeled `vibed.dev/sandbox-node: "true"`.
4. *(General lane only)* **A Kata `RuntimeClass`** — see [The general lane and Kata](#the-general-lane-and-kata). Static sites and other fast-lane deploys do **not** need Kata.
5. *(Production only)* **Object storage** (S3 or MinIO) for source tarballs, and a **DNS-01 capable DNS provider** for wildcard TLS — see [Going to production](#going-to-production).

## Step 1 — Install agent-sandbox

vibeD depends on agent-sandbox's controller and CRDs — the **core** (`Sandbox`) **and** the **extensions** (`SandboxTemplate`, `SandboxClaim`, `SandboxWarmPool`). Apply the upstream release manifests (v0.4.5+ required; substitute the version you want):

```bash
VER=v0.4.5
# core: controller + Sandbox CRD
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/$VER/manifest.yaml
# extensions: SandboxTemplate / SandboxClaim / SandboxWarmPool CRDs + controller
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/$VER/extensions.yaml
```

Confirm the extension CRDs are registered before continuing:

```bash
kubectl get crd sandboxtemplates.extensions.agents.x-k8s.io \
                sandboxwarmpools.extensions.agents.x-k8s.io
```

:::note
The kind testbed wraps these same manifests in a Helm chart (`testbed/agent-sandbox/`) that `make dev` uses. On a real cluster the raw `kubectl apply` above is simplest.
:::

## Step 2 — Install vibeD

vibeD ships as a Helm chart. The **minimal install** uses the in-cluster `served` source store and a ConfigMap metadata store — **no S3, no external database** — which is enough to evaluate on an unhardened cluster:

```bash
helm install vibed deploy/helm/vibed/ \
  -n vibed-system --create-namespace \
  --set controller.domain=apps.example.com
```

This installs the control plane (vibed, vibed-controller, vibed-router, Caddy) into `vibed-system` and one `SandboxWarmPool` per enabled runtime into the `vibed-apps` namespace. See [Going to production](#going-to-production) for S3 and hardening.

:::info Installing before agent-sandbox?
If the agent-sandbox **extensions** CRDs aren't present yet, `helm install` fails with `no matches for kind "SandboxTemplate"`. Either do [Step 1](#step-1--install-agent-sandbox) first (recommended), or install the control plane alone and enable the pools once agent-sandbox is ready:

```bash
helm install vibed deploy/helm/vibed/ -n vibed-system --create-namespace \
  --set controller.domain=apps.example.com --set warmPoolsEnabled=false
# …install agent-sandbox (Step 1), then:
helm upgrade vibed deploy/helm/vibed/ -n vibed-system --set warmPoolsEnabled=true
```
:::

### CRD upgrades

The `VibedApp` CRD ships in the chart's `crds/` directory. Helm installs it on first install but **never on `helm upgrade`**. After upgrading to a version that changes the CRD schema, re-apply it:

```bash
kubectl apply -f deploy/helm/vibed/crds/vibed.dev_vibedapps.yaml
```

## Verify

```bash
kubectl get pods -n vibed-system            # control plane Running
kubectl get sandboxwarmpool -n vibed-apps   # warm pools populated
```

Then deploy something — see [First deployment](first-deployment.md).

## The general lane and Kata

vibeD has two lanes. The **fast lane** (static sites, workerd) runs in ordinary pods and needs no special runtime — a first static deploy works with nothing extra. The **general lane** (Node/Python/Go/custom images) runs user code in **Kata microVMs** for VM-level isolation, which needs:

- A **Kata `RuntimeClass`** — `kata-qemu` (works without nested virt) or `kata-fc` (Firecracker, needs KVM). Select it with `runtime.defaultClass`.
- Sandbox nodes running `containerd` + `containerd-shim-kata-v2`. `kata-fc` needs KVM (bare metal, AWS `*.metal`, or nested-virt images on GCP); `kata-qemu` does not.

The easiest way to install Kata and register the RuntimeClasses is the [kata-deploy](https://github.com/kata-containers/kata-containers/tree/main/tools/packaging/kata-deploy) DaemonSet.

Until a Kata RuntimeClass exists, run only the fast lane: set `warmPoolsEnabled=false` (or enable just `static-nginx`). Leaving `runtime.defaultClass: ""` runs general-lane pods **without** Kata isolation — the dev default, not for production.

## Going to production

The minimal install above is fine for an eval; a hardened deployment also needs:

- **Source backend = `s3`.** The `served` backend only works while sandboxes have cluster DNS. In production sandboxes run under a restrictive NetworkPolicy with no cluster DNS, so the agent must pull a **pre-signed URL**:

  ```bash
  helm upgrade vibed deploy/helm/vibed/ -n vibed-system \
    --set config.storage.tarball.backend=s3 \
    --set config.storage.tarball.s3.bucket=vibed-sources \
    --set config.storage.tarball.s3.region=us-east-1
  ```

- **Sandbox NetworkPolicy** — `runtime.sandboxNetworkPolicy: Unmanaged` + `networkPolicy.enabled: true`, so vibeD owns a policy that permits exactly the control-plane → sandbox traffic plus DNS + S3 egress.
- **A Kata RuntimeClass** for the general lane (above).
- **HTTPS** via Caddy's DNS-01 wildcard cert on `*.<your-domain>`.
- **Pinned image tags** (not `latest`) and **auth enabled** before exposing the API.

See the [configuration reference](../configuration/config-reference.md) and the [production guide](../deployment/production-guide.md) for the full hardened setup, plus [storage](../configuration/storage.md) for S3/MinIO details.
