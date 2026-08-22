---
sidebar_position: 3
---

# Installation

This page installs vibeD on a real Kubernetes cluster (1.29+).

:::tip Just trying it out?
For a laptop / evaluation setup, use **[Local development](local-dev.md)**: a single `make dev` stands up kind, all dependencies, and vibeD. This page is for a shared or production cluster.
:::

## Prerequisites

1. **Kubernetes 1.29+** (EKS/GKE/AKS or on-prem), with cluster-admin to install CRDs.
2. **[agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox), core *and* extensions** (v0.5.0+). vibeD renders `SandboxTemplate` and `SandboxWarmPool` objects, so the **extensions** CRDs must exist before you install vibeD. [Step 1](#step-1--install-agent-sandbox) installs them.
3. **A sandbox node pool** labeled `vibed.dev/sandbox-node: "true"`.
4. *(General lane only)* **A Kata `RuntimeClass`**. [Step 2](#step-2--install-kata-containers-general-lane) installs it. Static sites and other fast-lane deploys do **not** need Kata.
5. *(Production only)* **Object storage** (S3 or MinIO) for source tarballs, and a **DNS-01 capable DNS provider** for wildcard TLS; see [Going to production](#going-to-production).

## Step 1: Install agent-sandbox {#step-1--install-agent-sandbox}

vibeD depends on agent-sandbox's controller and CRDs: the **core** (`Sandbox`) **and** the **extensions** (`SandboxTemplate`, `SandboxClaim`, `SandboxWarmPool`). Apply the upstream release manifests (**v0.5.0+ required**; that's the release that graduated the CRDs to `v1beta1`, which vibeD targets):

```bash
VER=v0.5.2
# core: controller + Sandbox CRD  (this file was named manifest.yaml before v0.5.0)
kubectl apply --server-side -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/$VER/sandbox.yaml
# extensions: SandboxTemplate / SandboxClaim / SandboxWarmPool CRDs + controller
kubectl apply --server-side -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/$VER/extensions.yaml
```

`--server-side` avoids the client-side apply annotation-size limit these CRDs now exceed. There's also a single all-in-one `sandbox-with-extensions.yaml` if you'd rather apply one file.

Confirm the extension CRDs are registered and serving `v1beta1` before continuing:

```bash
kubectl get crd sandboxtemplates.extensions.agents.x-k8s.io \
                sandboxwarmpools.extensions.agents.x-k8s.io \
                sandboxclaims.extensions.agents.x-k8s.io
# STORED VERSIONS should list v1beta1:
kubectl get crd sandboxwarmpools.extensions.agents.x-k8s.io \
  -o jsonpath='{.status.storedVersions}{"\n"}'
```

:::note Upgrading from agent-sandbox v0.4.x?
v0.5.0 promoted every CRD from `v1alpha1` to `v1beta1` (a conversion webhook still serves `v1alpha1`, so old objects keep working but log `… v1alpha1 SandboxWarmPool is deprecated; use … v1beta1 …`). vibeD v0.5.0+ writes `v1beta1` natively, which clears that warning. If you're on an older agent-sandbox, upgrade it to v0.5.0+ **before** upgrading vibeD; Helm never upgrades CRDs, so re-apply the manifests above and they'll patch in place.
:::

:::note
The kind testbed wraps these same manifests in a Helm chart (`testbed/agent-sandbox/`) that `make dev` uses. On a real cluster the raw `kubectl apply` above is simplest.
:::

## Step 2: Install Kata Containers (general lane) {#step-2--install-kata-containers-general-lane}

:::tip Fast-lane only? Skip this step.
Static sites and workerd deploys run in ordinary pods. Kata is only needed for the **general lane** (Node/Python/Go/custom images), which runs user code in a microVM. You can install it later and `helm upgrade` vibeD to switch the general lane on.
:::

**What Kata gives you.** [Kata Containers](https://katacontainers.io/) runs each sandbox's containers inside a lightweight virtual machine with its own guest kernel, launched by a hardware-virtualization hypervisor (QEMU, Cloud Hypervisor, or Firecracker). A container escape lands the attacker in a throwaway guest kernel, not on the node's shared host kernel, which is the isolation boundary the general lane relies on. The [sandbox isolation deep-dive](../concepts/sandbox-isolation.md) walks through how this maps to vibeD's lanes and how to configure it.

**Node requirement: hardware virtualization.** Every sandbox node needs `/dev/kvm`:

- **Bare metal** or AWS `*.metal`: KVM is present natively.
- **Cloud VMs**: enable **nested virtualization** (GKE nested-virt nodes, Azure `Dv5`/`Ev5`, etc.). Plain AWS EC2 VMs don't expose nested KVM; use a `*.metal` instance for the sandbox pool.

Install with **kata-deploy** (upstream's installer). It drops the runtime binaries + containerd shims onto the labeled sandbox nodes and registers the `RuntimeClass` objects:

```bash
KATA_VER=3.32.0
helm install kata-deploy \
  oci://ghcr.io/kata-containers/kata-deploy-charts/kata-deploy \
  --version "$KATA_VER" -n kube-system \
  --set nodeSelector."vibed\.dev/sandbox-node"=true
# k3s / rke2 / k0s / microk8s: also pass --set k8sDistribution=<distro> so the
# containerd config path resolves correctly.
```

The chart bundles node-feature-discovery and only installs on nodes advertising Intel VT-x / AMD-V, so a node without virtualization is skipped rather than silently broken. Restricting it to `vibed.dev/sandbox-node: "true"` keeps Kata off your control-plane and general workload nodes.

Wait for the DaemonSet, then confirm the RuntimeClasses exist:

```bash
kubectl -n kube-system rollout status ds/kata-deploy
kubectl get runtimeclass          # expect kata-qemu, kata-fc, kata-clh, …
```

**Point vibeD at a RuntimeClass.** The general lane runs pods under whatever `runtime.defaultClass` names; the fast lane ignores it. You set this in [Step 3](#step-3--install-vibed) (or with a later `helm upgrade`):

```bash
# QEMU/KVM: the default, works anywhere /dev/kvm is present:
--set runtime.defaultClass=kata-qemu
# …or Firecracker microVMs (smaller/faster; needs the devmapper snapshotter on the node):
# --set runtime.defaultClass=kata-fc
```

Leaving `runtime.defaultClass: ""` runs general-lane pods **without** Kata (shared-kernel runc), the dev/kind default, never production.

## Step 3: Install vibeD {#step-3--install-vibed}

vibeD ships as a Helm chart. The **minimal install** uses the in-cluster `served` source store and a ConfigMap metadata store (**no S3, no external database**), which is enough to evaluate on an unhardened cluster:

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

Then deploy something; see [First deployment](first-deployment.md).

## The general lane and Kata

vibeD has two lanes. The **fast lane** (static sites, workerd) runs in ordinary pods and needs no special runtime, so a first static deploy works with nothing extra. The **general lane** (Node/Python/Go/custom images) runs user code in **Kata microVMs** for VM-level isolation. [Step 2](#step-2--install-kata-containers-general-lane) installs Kata and registers the `RuntimeClass`; the general lane then runs under whatever `runtime.defaultClass` names (`kata-qemu` or `kata-fc`). Both hypervisors need `/dev/kvm` on the node; there's no software-emulation shortcut for production.

Until a Kata RuntimeClass exists, run only the fast lane: set `warmPoolsEnabled=false` (or enable just `static-nginx`). Leaving `runtime.defaultClass: ""` runs general-lane pods **without** Kata isolation, the dev default, not for production. For how the isolation boundary actually works and how to tune the microVM (kernel, memory, Firecracker vs QEMU), see the [sandbox isolation deep-dive](../concepts/sandbox-isolation.md).

## Going to production

The minimal install above is fine for an eval; a hardened deployment also needs:

- **Source backend = `s3`.** The `served` backend only works while sandboxes have cluster DNS. In production sandboxes run under a restrictive NetworkPolicy with no cluster DNS, so the agent must pull a **pre-signed URL**:

  ```bash
  helm upgrade vibed deploy/helm/vibed/ -n vibed-system \
    --set config.storage.tarball.backend=s3 \
    --set config.storage.tarball.s3.bucket=vibed-sources \
    --set config.storage.tarball.s3.region=us-east-1
  ```

- **Sandbox NetworkPolicy**: `runtime.sandboxNetworkPolicy: Unmanaged` + `networkPolicy.enabled: true`, so vibeD owns a policy that permits exactly the control-plane → sandbox traffic plus DNS + S3 egress.
- **A Kata RuntimeClass** for the general lane (above).
- **HTTPS** via Caddy's DNS-01 wildcard cert on `*.<your-domain>`.
- **Pinned image tags** (not `latest`) and **auth enabled** before exposing the API.

See the [configuration reference](../configuration/config-reference.md) and the [production guide](../deployment/production-guide.md) for the full hardened setup, plus [storage](../configuration/storage.md) for S3/MinIO details.
