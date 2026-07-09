---
sidebar_position: 5
---

# Custom Base Images (BYO)

vibeD ships a base image per language slot (`node-24`, `python-313`, `go-123`, `base-al2023`, `static-nginx`). You can replace any of them with **your own** image — for example a hardened, CVE-scanned, or internally-blessed base — point vibeD at it, and vibeD will **validate** it before routing real traffic to it.

You don't add new *languages* this way; you swap the image that backs an existing language slot. The classifier still routes (e.g.) Node apps to the `node-24` slot — your image just *is* `node-24` now.

## How vibeD validates an image

When you register a custom image, the controller boot-checks it against a contract and **hard-gates** deploys whose image fails:

1. The image's `vibed-agent` answers `GET /info` on `:9000` (it embeds and runs the agent).
2. The image declares the language the slot expects (e.g. a `node-24` image must declare `node`).
3. The declared **runtime probe** actually succeeds (e.g. `node --version` exits 0) — proving the runtime is really present.

If validation fails, apps routed to that slot go **`Failed`** with a `TemplateInvalid` condition and the exact reason (wrong language, missing runtime, no agent), instead of silently breaking. Slots not yet validated are allowed through (the validator re-runs every 2 minutes).

## Creating your own base image

Your image must satisfy the contract below. The simplest path is to start `FROM` your hardened base and add the agent + contract:

```dockerfile
# Stage 1: grab a statically-linked vibed-agent (or COPY one you've vendored).
FROM ghcr.io/vibed-project/vibed-runner-node:0.5.0 AS agent

# Stage 2: your hardened base.
FROM your-registry/hardened-node:24

# 1. The agent binary + the language entrypoint, run under tini as PID 1.
COPY --from=agent /usr/local/bin/vibed-agent /usr/local/bin/vibed-agent
COPY --from=agent /etc/vibed/entrypoint.sh   /etc/vibed/entrypoint.sh

# 2. Declare the contract: the language vibeD routes here + a probe that
#    proves the runtime exists. BOTH the label (static inspection) and the
#    env (read by the agent's /info) are required.
LABEL dev.vibed.template.language="node" \
      dev.vibed.agent.contract="1"
ENV VIBED_TEMPLATE_LANGUAGE=node \
    VIBED_RUNTIME_PROBE="node --version" \
    VIBED_AGENT_WORKDIR=/workspace \
    VIBED_AGENT_CONTROL_ADDR=:9000 \
    VIBED_AGENT_APP_PORT=8080

# 3. Non-root + the control/app ports.
USER 1000
EXPOSE 9000 8080
ENTRYPOINT ["tini", "--", "vibed-agent"]
```

**Contract checklist:**

| Requirement | Detail |
|---|---|
| `vibed-agent` as PID 1 | At `/usr/local/bin/vibed-agent`, under `tini`; control API on `:9000`, app on `:8080`. |
| `/etc/vibed/entrypoint.sh` | The language's run convention (the agent invokes it after extracting source). Reuse the shipped one for the language, or write your own. |
| `dev.vibed.template.language` label | `node` / `python` / `go` / `static` / `base`. Must match the slot. |
| `VIBED_TEMPLATE_LANGUAGE` env | Same value; the agent reports it via `/info`. |
| `VIBED_RUNTIME_PROBE` env | A command (run via `sh -c`) that proves the runtime, e.g. `node --version`. Empty ⇒ validation fails. |
| Non-root, read-only rootfs friendly | Runs as UID 1000; write only to `/workspace` and `/tmp`. |

## Registering a custom image

Point the slot at your image in Helm values:

```yaml
warmPools:
  node-24:
    enabled: true
    image: your-registry/hardened-node-vibed:1.0
    imagePullSecret: my-registry-creds   # optional, for a private registry
    replicas: 30
```

`imagePullSecret` must reference a `kubernetes.io/dockerconfigjson` Secret in the workloads namespace (`vibed-apps`). After `helm upgrade`, the controller validates the new image on its next pass. Check the result with the CLI:

```bash
vibed validate-images
# TEMPLATE  VALID  IMAGE                              DETAIL
# node-24   yes    your-registry/hardened-node...     node
```

or the raw status / metric:

```bash
kubectl get configmap vibed-template-validation -n vibed-apps -o jsonpath='{.data.node-24}'
# {"template":"node-24","image":"...","language":"node","valid":true,"checkedAt":"..."}
# Prometheus (controller :8081): vibed_template_validation{template="node-24",result="invalid"}
```

## Deactivating default images

Disable any built-in slot you don't want (e.g. you only allow your own hardened images, or your cluster shouldn't run Go apps):

```yaml
warmPools:
  go-123:      { enabled: false }
  base-al2023: { enabled: false }
```

A disabled slot has no warm pool, so apps the classifier routes there fail to claim. Either provide a replacement image for that slot or accept that the language is unsupported on your install.

## Turning validation off

Validation is on by default. For air-gapped or fully-trusted installs you can skip the preflight (the claim path then allows every slot):

```yaml
templateValidation:
  enabled: false
```
