---
sidebar_position: 2
---

# Lanes & Templates

vibeD exposes a uniform API. You don't pick a runtime — a deterministic **classifier** inspects the uploaded source and routes it to a **lane** and a **template**. You *can* override either via `runtime.lane` / `runtime.template` in the deploy metadata, but that's the exception.

## Lanes

| Lane | Isolation | Used for |
|---|---|---|
| **fast** | V8 isolates (workerd) or static nginx | Static sites and small trusted-language workers. Sub-second cold start. |
| **general** | Kata **microVM** (QEMU or Firecracker) | Arbitrary code — Node, Python, Go, or any image. Hardware-grade isolation. |

The fast lane has two flavors: `static-nginx` (a sandbox pod serving `/workspace`) and `workerd` (V8 isolates managed by a loader). The general lane always runs on a Kata microVM sandbox.

## The classifier

`internal/classifier` is a pure, deterministic function that reads only file *names* and `package.json` top-level keys — it never installs anything and runs in well under 50 ms. Rules run in order, first match wins:

1. **No server-side code** (only `.html`/`.css`/`.js`/images, no `package.json`/`requirements.txt`/`Dockerfile`) → **fast**, `static-nginx`.
2. **`package.json` with only browser deps + a `build` script** → build asynchronously, serve the output from `static-nginx`.
3. **`worker.js` / `worker.ts` / `wrangler.toml`** → **fast**, `workerd`.
4. **`package.json`** (Node app with deps) → **general**, `node-24`.
5. **`requirements.txt` / `pyproject.toml`** → **general**, `python-313`.
6. **`go.mod`** → **general**, `go-123`.
7. **`Dockerfile` at root** → **general**, `base-al2023` (the Dockerfile is a hint for the start command, *not* built).
8. **Else** → **general**, `base-al2023` with entrypoint autodetection.

## Templates

A template is a directory under `templates/` with a `Dockerfile`, an `entrypoint.sh`, and a `template.yaml` (the `SandboxTemplate` + `SandboxWarmPool` manifests). Template images are built by CI **on template changes, never per deploy**, and each embeds `vibed-agent` as PID 1.

The shipped template set:

| Template | Base | Lane | Default warm pool |
|---|---|---|---|
| `node-24` | `node:24` | general | 5 |
| `python-313` | `python:3.13` | general | 5 |
| `go-123` | `golang:1.23` | general | 5 |
| `base-al2023` | `amazonlinux:2023` | general | 5 |
| `static-nginx` | `nginx:alpine` | fast | 5 |

:::note
`refactor.md` lists additional templates (`node-22`, `bun-1`, `deno-2`) as design targets. The five above are what currently ship; the others are not yet built. Every pool defaults to **5** warm replicas in `values.yaml`; size each one to your expected concurrent-deploy burst per runtime (see [Warm pools](sandbox-isolation.md#warm-pools-how-vibed-hides-microvm-boot-latency) in the isolation deep-dive).
:::

## Source injection, not image builds

The deploy path never builds an image. The user's source is a gzipped tarball; `vibed-agent` extracts it into `/workspace` inside a pre-booted sandbox and starts the process. New runtime/dependency combinations are handled by an **async template builder** that refreshes the warm pool out of band — it never blocks a user-visible deploy.
