---
sidebar_position: 1
slug: /
---

# Introduction

**vibeD** lets an AI agent (Claude Desktop, Cursor, Goose, or any MCP client) deploy a small web app **in seconds** and get back a URL, running on *your* Kubernetes cluster instead of the agent's sandbox.

The target user is an enterprise employee who vibe-coded a small tool with GenAI. vibeD captures that app into a centrally governed, hardware-isolated sandbox instead of leaving it on the employee's laptop or a third-party host.

## What makes it fast

vibeD never builds a container image on the deploy path. Instead it keeps **warm pools** of pre-booted sandboxes for each runtime, and on deploy it *claims* one and injects the user's source tarball into it. No `docker build`, no `buildah`, no image push between you and your URL.

```
   POST /v1/deploy  (source tarball + metadata)
        │
        ▼
   ┌──────────┐   classify    ┌──────────────┐   claim     ┌──────────────────┐
   │ vibed    │──── lane ─────▶│ VibedApp CR  │────────────▶│ warm sandbox pool │
   │ (server) │   + template   │ (controller) │  + inject   │ (agent-sandbox)   │
   └──────────┘                └──────────────┘   source    └──────────────────┘
        ▲                                                            │
        │                  ┌────────┐    program route               │ Ready
        └── URL ───────────│ Caddy  │◀──── vibed-router ─────────────┘
                           └────────┘
```

## Two lanes, one API

The HTTP API is uniform. A deterministic **classifier** inspects the source and picks a lane; you never choose a runtime by hand (though you can override):

- **fast lane**: V8 isolates (workerd) and static sites. Sub-second, for trusted-language workloads.
- **general lane**: Kata **microVMs** (QEMU by default, Firecracker on KVM) for hardware-grade isolation of arbitrary code (Node, Python, Go, or any image).

## Key components

- **vibed**: stateless HTTP server: auth, tarball upload, classifier, creates the `VibedApp` CR, serves the dashboard + MCP server.
- **vibed-controller**: reconciles `VibedApp` → an [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) `SandboxClaim`, injects source, drives the app to `Ready`.
- **vibed-router**: watches `VibedApp` status and programs **Caddy** routes (wildcard DNS-01 TLS in prod).
- **Caddy**: the reverse proxy fronting every app at `https://<id>.<your-domain>`.

vibeD owns exactly **one** CRD: [`VibedApp`](concepts/app-lifecycle.md). Everything else reuses agent-sandbox's `Sandbox` / `SandboxTemplate` / `SandboxClaim` / `SandboxWarmPool`.

## Where to go next

- [Architecture](concepts/architecture.md): the components and the deploy flow in detail.
- [Lanes & templates](concepts/lanes-and-templates.md): how the classifier routes apps.
- [Local development](getting-started/local-dev.md): run the whole stack on Kind.
- [Installation](getting-started/installation.md): deploy vibeD to a real cluster.
