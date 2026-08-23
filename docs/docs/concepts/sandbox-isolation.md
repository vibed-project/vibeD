---
sidebar_position: 6
---

# Sandbox Isolation: agent-sandbox & Kata

This page is for the **platform engineer** standing up vibeD on a shared or multi-tenant cluster who hasn't worked with [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) or [Kata Containers](https://katacontainers.io/) before. It explains what each one is, the isolation each provides, how they compose inside vibeD, and how to drive and tune them.

If you just want the install commands, see [Installation → Step 2](../getting-started/installation.md#step-2--install-kata-containers-general-lane). This page is the "why" and the "how it actually works" behind that step.

## The one thing to get straight first

agent-sandbox and Kata solve **different problems**, and vibeD uses both:

| | agent-sandbox | Kata Containers |
|---|---|---|
| **Layer** | Orchestration (Kubernetes CRDs + controller) | Runtime (a container runtime under containerd) |
| **Job** | Keep pools of pre-booted sandboxes warm; lease one per app; track its lifecycle | Run a pod's containers inside a hardware VM instead of sharing the host kernel |
| **Gives you** | *Speed*: a deploy binds to an already-running sandbox instead of cold-starting one | *Isolation*: a kernel-level VM boundary between untrusted code and the host |
| **Selected via** | `SandboxTemplate` / `SandboxWarmPool` / `SandboxClaim` objects vibeD renders | A Kubernetes `RuntimeClass` (`kata-qemu`, `kata-fc`) named on the pod |

**agent-sandbox does not isolate anything by itself**; it schedules pods. The isolation strength of those pods is entirely determined by their `RuntimeClass`. Point a SandboxTemplate at `runc` (the default) and you get an ordinary shared-kernel container; point it at a Kata RuntimeClass and the same sandbox becomes a microVM. vibeD wires these together so that in production **every warm-pool sandbox** runs under a Kata RuntimeClass; see [How vibeD wires the two together](#how-vibed-wires-the-two-together).

## agent-sandbox: warm, leasable sandboxes

agent-sandbox is a Kubernetes-SIG project that models "a sandbox for agent/code workloads" as first-class API objects. vibeD renders four kinds across two API groups (all `v1beta1` since agent-sandbox v0.5.0):

```
agents.x-k8s.io/v1beta1
  Sandbox              one sandbox = one pod with a stable identity + lifecycle
                       (operatingMode: Running | Suspended)

extensions.agents.x-k8s.io/v1beta1
  SandboxTemplate      a reusable pod spec ("what a node-24 sandbox looks like")
  SandboxWarmPool      keep N Ready sandboxes from a template idle and waiting
  SandboxClaim         "give me one warm sandbox from pool X" → binds a pod
```

The agent-sandbox **controller** reconciles these: a `SandboxWarmPool` with `replicas: 5` keeps five pre-booted, Ready pods sitting idle; a `SandboxClaim` leases one of them and publishes the bound pod's identity under `status.sandbox.{name,podIPs}`. Deleting the claim releases the pod back toward the pool.

### How vibeD uses them

Every shipped template (`templates/<name>/`) carries a `SandboxTemplate` + `SandboxWarmPool` pair, rendered by the Helm chart into the pools namespace (`namespaces.apps`, default `vibed-apps`):

```
deploy → classifier picks a template (e.g. node-24)
       → controller creates a SandboxClaim  (spec.warmPoolRef.name: node-24)
       → agent-sandbox binds a warm node-24 pod, reports status.sandbox
       → vibed-agent (PID 1 in that pod) unpacks your tarball into /workspace and runs it
```

vibeD's claim path targets `SandboxClaim` in `extensions.agents.x-k8s.io/v1beta1` and sets **`spec.warmPoolRef.name`** to the template name. (Before agent-sandbox v0.5.0 this was two fields, `sandboxTemplateRef` + `warmpool`, on the `v1alpha1` claim; see the upgrade note below.)

:::note The v0.5.0 API bump (`v1alpha1` → `v1beta1`)
agent-sandbox v0.5.0 graduated every CRD to `v1beta1`. A **conversion webhook** (served by the `agent-sandbox-controller` itself, which self-signs and injects its own `caBundle`, so **no cert-manager is required**) still accepts `v1alpha1`, so old objects keep working but emit `… v1alpha1 SandboxWarmPool is deprecated; use … v1beta1 …`. vibeD v0.5.0+ writes `v1beta1` natively, which clears the warning. Two operational consequences: (1) the controller must be `Available` before the API can convert or admit objects, and (2) because there's a conversion webhook, apply the CRDs with `kubectl apply --server-side`.
:::

### Warm pools: how vibeD hides microVM boot latency

A Kata microVM takes longer to start than a plain container (a guest kernel has to boot). If every deploy paid that cost, the general lane would feel sluggish. Warm pools move the cost **off the request path**: agent-sandbox keeps a set of microVMs already booted and Ready, so a deploy is a *lease*, not a *boot*.

Each pool defaults to **5** replicas (`warmPools.<pool>.replicas` in `values.yaml`). Size each pool to your expected **concurrent-deploy burst per runtime**, not your total app count; a bound sandbox leaves the pool and the controller replenishes it:

- Bursty CI that redeploys 20 Node apps at once → raise `node-24` toward 20.
- A runtime you rarely use → drop it to 1–2 (or disable the pool).
- Every warm replica is an idle microVM consuming its `requests` (memory especially). Warm pools trade **standing cost for tail latency**; tune with that in mind.

## Kata Containers: a VM boundary per sandbox

An ordinary container (runc) is just a set of Linux **namespaces + cgroups + seccomp** around a process that shares the **host kernel**. That shared kernel is a large attack surface: a kernel bug or a container escape reachable from a syscall can put untrusted code on the node.

Kata runs each pod's containers inside a **lightweight virtual machine** with its **own guest kernel**, launched by a hardware hypervisor. The pieces:

```
containerd
  └─ containerd-shim-kata-v2        the shim containerd talks to
       └─ hypervisor (QEMU | Cloud Hypervisor | Firecracker)
            └─ guest kernel + minimal rootfs
                 └─ kata-agent      runs your container(s) inside the guest
                      └─ user-container (your code)
```

Now a container escape lands the attacker in a **throwaway guest kernel inside a VM**, not on the host. The security boundary becomes the **hypervisor**, a far smaller, purpose-hardened surface than the full Linux syscall ABI. This is the isolation the general lane depends on when it runs arbitrary user code.

### Where the lanes sit on the isolation spectrum

| Runtime | Kernel | Enforced by | Overhead | vibeD |
|---|---|---|---|---|
| **runc** (default) | shared host kernel | namespaces, cgroups, seccomp | ~none | Kind/dev only (`runtime.defaultClass=""`) |
| gVisor | user-space kernel (Sentry) | syscall interception | low–med | not used |
| **Kata** (QEMU/FC/CLH) | dedicated guest kernel per pod | **hypervisor / VM** | med | **all warm-pool sandboxes** in prod (general lane + `static-nginx`) |
| VM per tenant | dedicated kernel + full OS | hypervisor | high | not vibeD's model |

The general lane runs whatever the user uploaded, so it **always** gets the VM boundary. The `static-nginx` fast-lane pool is a warm-pool sandbox too, so in the shipped chart it inherits the same Kata RuntimeClass, cheap defense-in-depth for user-supplied assets. The one fast path that never uses a microVM is **`workerd`**: user code there is confined to a **V8 isolate** by the workerd runtime rather than to a pod, so it isn't a SandboxWarmPool at all.

### Hardware requirement: `/dev/kvm`

Every Kata hypervisor needs hardware virtualization, i.e. **`/dev/kvm` on the sandbox node**. There is no production-grade software-emulation fallback.

- **Bare metal** / AWS `*.metal`: KVM is present natively.
- **Cloud VMs**: you must enable **nested virtualization** (GKE nested-virt node images, Azure `Dv5`/`Ev5`, etc.). Plain AWS EC2 VMs do **not** expose nested KVM; use a `*.metal` instance for the sandbox pool.

kata-deploy bundles node-feature-discovery and only installs on nodes advertising Intel VT-x / AMD-V, so a non-virt-capable node is skipped rather than silently broken.

### Choosing a hypervisor

kata-deploy registers a `RuntimeClass` per hypervisor. The two vibeD documents:

| RuntimeClass | Hypervisor | Boot | Footprint | Notes |
|---|---|---|---|---|
| `kata-qemu` | QEMU/KVM | ~hundreds of ms | larger | Mature, broadest device support. vibeD's **default** (`runtime.defaultClass: kata-qemu`). |
| `kata-fc` | Firecracker | ~125 ms | minimal | AWS's minimal VMM; small attack surface, fast. **No device hotplug → requires the `devmapper` snapshotter** (block-based rootfs) in containerd. |

`kata-clh` (Cloud Hypervisor) is a third option: a modern Rust VMM with hotplug support, a middle ground between the two. Start with `kata-qemu` for the widest compatibility; move to `kata-fc` when you want the smallest footprint and fastest boot and have devmapper configured.

## How vibeD wires the two together

The join point is one field on the SandboxTemplate's pod spec:

```yaml
# templates/node-24/template.yaml (rendered by the chart)
kind: SandboxTemplate
spec:
  podTemplate:
    spec:
      runtimeClassName: kata-qemu     # ← set from runtime.defaultClass
      containers: [ ... ]             #   omitted entirely when defaultClass is ""
```

The Helm chart sets `runtimeClassName` on **every warm-pool pod** (all five pools, the four general-lane templates *and* `static-nginx`) from `runtime.defaultClass`. So:

- `runtime.defaultClass: kata-qemu` (or `kata-fc`) → general-lane sandboxes are microVMs.
- `runtime.defaultClass: ""` → the field is omitted, pods schedule under the cluster default (**runc, no VM isolation**). This is the **Kind/dev default** (`values-kind.yaml`) because Kind nodes have no `/dev/kvm`. **Never run production this way.**

## Driving it: explicit use

### Pick the isolation runtime

```bash
# QEMU/KVM (default): works anywhere /dev/kvm is present:
helm upgrade vibed deploy/helm/vibed/ -n vibed-system --set runtime.defaultClass=kata-qemu
# Firecracker microVMs (needs devmapper on the node):
helm upgrade vibed deploy/helm/vibed/ -n vibed-system --set runtime.defaultClass=kata-fc
```

### Force a deploy onto the general (Kata) lane

The classifier auto-routes, but you can override per deploy via the metadata:

```jsonc
{ "runtime": { "lane": "general", "template": "base-al2023" } }
```

A static site that would normally take the fast lane can be pushed into a Kata microVM this way, useful when even "static" content is untrusted.

### Confirm a sandbox really is a microVM

Isolation bugs are silent: a mis-set `runtime.defaultClass` downgrades you to runc with no error. Verify from inside a running general-lane sandbox:

```bash
# The guest kernel differs from the node kernel, and virt is detected:
kubectl exec <sandbox-pod> -n vibed-apps -- uname -r
kubectl exec <sandbox-pod> -n vibed-apps -- systemd-detect-virt   # → qemu / kata (not "none")
# On the node, the pod's RuntimeClass should be a kata-* class, not runc:
kubectl get pod <sandbox-pod> -n vibed-apps -o jsonpath='{.spec.runtimeClassName}{"\n"}'
```

If `systemd-detect-virt` returns `none` or `runtimeClassName` is empty, general-lane workloads are **not** VM-isolated.

## Configuring the microVM

### RuntimeClass ownership and node pinning

By default vibeD does **not** create the RuntimeClass (`runtime.installRuntimeClass: false`); kata-deploy owns it. If you'd rather Helm manage a thin RuntimeClass (e.g. to attach scheduling), set `installRuntimeClass: true`; `runtime.handler` defaults to `defaultClass`. Use the RuntimeClass's `scheduling` (nodeSelector + tolerations) to guarantee microVMs only land on KVM-capable sandbox nodes:

```yaml
# a RuntimeClass with scheduling pins every Kata pod to the sandbox pool
scheduling:
  nodeSelector: { vibed.dev/sandbox-node: "true" }
  tolerations: [ { key: vibed.dev/sandbox, operator: Exists, effect: NoSchedule } ]
```

Kubernetes merges that selector/toleration into every pod using the class, so you don't repeat it on each SandboxTemplate.

### Tuning the guest (vCPUs, memory, kernel)

kata-deploy ships a `configuration.toml` per hypervisor (default vCPUs, memory, guest kernel, image). Two ways to override:

- **Per workload** via Kata pod annotations on the SandboxTemplate's `podTemplate.metadata.annotations`, e.g. `io.katacontainers.config.hypervisor.default_memory: "512"`. Only annotations enabled in the Kata config are honored (there's an allow-list, so don't expect every knob to be settable from a pod by default).
- **Cluster-wide** by editing the kata-deploy config / mounting a custom `configuration.toml`, then referencing it from a dedicated RuntimeClass handler.

### Firecracker's devmapper requirement

`kata-fc` has no device hotplug, so it can't use the default overlayfs snapshotter; it needs containerd's **`devmapper`** snapshotter for a block-based rootfs. kata-deploy can configure this, but it requires a thin-pool device on the node; budget for that before switching a pool to `kata-fc`.

### Warm-pool and template knobs (agent-sandbox side)

- `warmPools.<pool>.replicas`: idle microVMs per runtime (default **5**).
- `warmPools.<pool>.image` / `.resources`: the template image and per-sandbox requests/limits. Requests matter: `replicas × requests.memory` is standing reserved memory.
- `warmPoolsEnabled: false`: bring up the control plane with no pools (e.g. before Kata exists), then re-enable with a `helm upgrade`.

## Gotchas checklist

- **No `/dev/kvm`** → Kata pods fail to start. Cloud VMs need nested virt; plain AWS EC2 needs `*.metal`.
- **`runtime.defaultClass: ""` in production** → general lane silently runs on runc. Verify with `systemd-detect-virt`.
- **Switching to `kata-fc` without devmapper** → sandboxes won't boot. Configure the snapshotter first.
- **Controller not Ready after a v0.5.x install/upgrade** → the conversion webhook can't serve, so claims/templates fail to admit. Wait for `agent-sandbox-controller` to be `Available`.
- **Memory pressure** → warm pools reserve `replicas × requests.memory` per runtime *plus* the guest-kernel overhead of each microVM. Size sandbox nodes accordingly, or trim pools.
- **Helm never upgrades CRDs** → after bumping agent-sandbox, re-`kubectl apply --server-side` its manifests.

## Further reading

- [Lanes & Templates](lanes-and-templates.md): how the classifier routes to fast vs general.
- [Installation → Step 1 & 2](../getting-started/installation.md#step-1--install-agent-sandbox): the concrete install commands.
- [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) and [Kata Containers docs](https://github.com/kata-containers/kata-containers/tree/main/docs) upstream.
