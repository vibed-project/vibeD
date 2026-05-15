---
sidebar_position: 4
---

# Instant Preview

**Instant Preview** is vibeD's fast path for "little code apps" — Python and
Node.js apps that you want to *see running* immediately, without waiting for a
container build.

## Why it exists

The normal deploy path builds a container image for every request: a Buildah
Kubernetes Job runs `buildah bud` + `buildah push`, which takes minutes. That's
the right tradeoff for a durable, reproducible artifact — but it's slow when you
just want to glance at the result of a code change.

Instant Preview removes the build from the request path entirely.

## How it works

vibeD keeps a **warm pool of runner pods** — one small sub-pool per language,
each pod a generic language image (`vibed-runner-python`, `vibed-runner-node`)
that's already running and idle. A pod bundles the interpreter, a curated set of
**pre-baked dependencies**, and a small **runner agent** that exposes an
in-cluster control API.

When you deploy an eligible app:

1. vibeD **claims** an idle runner from the pool (or creates a cold one on
   demand if the pool is empty — a demand spike never fails a deploy).
2. It **injects** your source into the runner over the agent's control API.
3. The agent starts your process; vibeD confirms it stays up and returns the
   URL.

No image build. The pool is topped up in the background, so the next deploy has
a warm runner waiting. A runner that has run your code is **never reused** for
another app — on delete it's destroyed and replaced with a fresh one.

## The dependency gate

Instant Preview only applies when every dependency your app declares is
**pre-baked into the runner image**. vibeD parses your `requirements.txt` /
`package.json` and checks each dependency against the runner image's manifest:

- **All dependencies pre-baked** (or none declared) → Instant Preview.
- **Any dependency missing** → vibeD silently falls back to the full container
  build path.

This keeps the fast path genuinely instant — there's no dependency install step
hiding in it.

## Preview → Promote

An Instant Preview deploy is **ephemeral** — `mode: preview` on the artifact. It
runs on a pooled pod, and the garbage collector reaps it after `previewTTL`.

To make it durable, **promote** it (see
[`promote_artifact`](../mcp-tools/promote-artifact.md)): vibeD runs the real
container build, deploys the digest-pinned image to a production backend
(Knative / Kubernetes / Sandbox), swaps the live deployment, and recycles the
pooled runner. The `artifact_id` is stable across the promote — the URL changes,
so re-fetch it with `get_artifact_status`.

Set `fastPath.autoPromote: true` to promote every preview automatically in the
background: you see the preview instantly, and the durable build materializes
moments later.

## Reaching a preview

The runner pod itself only has an in-cluster URL
(`http://<runner>.<ns>.svc.cluster.local:8080`), so vibeD exposes a reverse
proxy on its own HTTP listener:

```
GET <server.baseURL>/preview/<artifact_id>/<path>
  → vibeD proxies to the runner pod's app port
```

When `server.baseURL` is set, `artifact.url` is automatically populated with the
proxy URL (e.g. `http://localhost:8080/preview/abc123/`) — paste it into a
browser or share it. Auth flows through the standard middleware; the artifact
owner (or share-grantee, or admin) can reach the preview, no one else.

vibeD strips its own `Authorization` and `Cookie` headers before proxying, so
the user app never sees them, and sets standard `X-Forwarded-Host`,
`X-Forwarded-Proto`, and `X-Forwarded-Prefix` so frameworks that honour them
(Werkzeug `ProxyFix`, FastAPI `root_path`, Express) reconstruct the correct
external URL.

:::caution Sub-path mounting
The preview is mounted under `/preview/<artifact_id>/`, but the user app
inside the runner serves at `/`. Apps that emit **absolute paths** (e.g. an
`<img src="/logo.png">` or a `Location: /home` redirect) won't resolve under
the preview prefix unless the app honours `X-Forwarded-Prefix` or is otherwise
prefix-aware. The simple-app case (relative URLs, framework templates) works
out of the box; for complex apps the right answer is usually to **promote**
the preview to a real subdomain via Knative.
:::

## Enabling it

Instant Preview is **disabled by default** and requires the
[agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) CRD in the
cluster. Enable it under `config.fastPath` (see the
[configuration reference](../configuration/config-reference.md)) — at minimum,
`enabled: true` and a `runners` entry per language with the runner image to use.
