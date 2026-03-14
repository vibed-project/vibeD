---
slug: the-vibed-idea
title: "The idea for vibeD"
authors: [max]
tags: [article, vision, thoughts]
---

AI is taking its place — in work, in private life, and everywhere in between. With this comes a couple of challenges: many are already in sight, others we can't yet imagine.

For corporates and businesses, an AI-enabled employee gains the capability to create artifacts like websites, apps, and more. Right now, these creations are either running within the context of the provider tool (a desktop app, a browser tab) or are clumsy to deploy — requiring support from an IT expert who understands containers, Kubernetes, and CI/CD pipelines.

**vibeD** enters the stage.

The idea of **vibeD** is to provide an endpoint for any GenAI tool to deploy artifacts under your own conditions, in a controllable environment. No deployment competencies needed for your non-IT personnel. Extensible, secure, and built on standards.

<!-- truncate -->

## What is vibeD?

In short: vibeD bridges the gap between AI code generation and production deployment. It exposes an [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) server that AI agents can call directly, handling the entire lifecycle from source files to running services.

An AI coding assistant like Claude, Gemini, or ChatGPT generates a website or an API. Then what? Today, the user copies files around, writes a Dockerfile, sets up a CI pipeline, configures Kubernetes manifests — or simply gives up and shares a screenshot. vibeD eliminates all of that. The AI agent calls `deploy_artifact` with the source files, and vibeD takes care of building, deploying, and serving the application. The user gets back a URL. Done.

## vibeD's foundation

### Kubernetes and Knative

vibeD is built on the shoulders of the cloud native ecosystem. At its core, it leverages **Kubernetes** as the universal runtime, an  infrastructure layer that most organizations already operate or have access to.

On top of Kubernetes, vibeD integrates with **Knative Serving** for serverless workloads. Knative brings scale-to-zero, automatic TLS, and revision-based traffic management. For artifacts that don't need to run 24/7 (and most AI-generated prototypes don't), this means zero cost when idle and instant startup when someone visits the URL.

For teams that don't have Knative installed, vibeD falls back gracefully to plain **Kubernetes Deployments** — no special CRDs required. And for compiled languages like Go and Rust, vibeD can deploy to **wasmCloud** as lightweight WebAssembly components.

This multi-target approach is intentional: vibeD meets your infrastructure where it is, rather than demanding a specific stack.

### MCP and coding agent integration

The [Model Context Protocol](https://modelcontextprotocol.io/) is what makes vibeD fundamentally different from yet another deployment tool. MCP is an open standard that allows AI models to interact with external tools in a structured way. The AI discovers what tools are available, understands their input schemas, and calls them autonomously.

vibeD exposes [11 MCP tools](pathname:///vibeD/docs/mcp-tools/overview) that cover the full artifact lifecycle: deploy, update, list, inspect, rollback, share, and delete. An AI agent doesn't need to know about Docker, Kubernetes, or networking. It just calls `deploy_artifact` with the files it generated and gets a URL back.

This means **any MCP-compatible AI tool** can deploy to your infrastructure — Claude Desktop, VS Code with Copilot, custom agents built with the Agent SDK, or tools that don't even exist yet. vibeD is the bridge between "AI generated code" and "running application", and MCP is the protocol that makes it universally accessible.

## vibeD's architecture

vibeD is a single Go binary. No microservices, no sidecars, no complex operator patterns. Possibilities to improve that if needed, but for now it serves its purpose. It runs as one container in your cluster and serves three interfaces simultaneously:

1. **MCP Server** - the protocol endpoint for AI tools (via stdio or HTTP)
2. **REST API** — a JSON API for programmatic access and the dashboard
3. **Web Dashboard** — a React SPA for humans to monitor and manage artifacts

Under the hood, vibeD follows an interface-driven architecture. Every subsystem like storage, building, deploying, state management, is behind a Go interface with multiple implementations. This means you can swap out the artifact store (in-memory, ConfigMap, or SQLite), the storage backend (local filesystem, GitHub, or GitLab), or the deployment target (Knative, Kubernetes, or wasmCloud) without changing any business logic.

The build process uses **Buildah**, running as Kubernetes Jobs inside the cluster. vibeD auto-generates the right Dockerfile based on the detected language (Node.js, Python, Go, Rust, or static HTML) so the user (and the AI) never needs to write one.

Real-time updates flow through an in-memory **EventBus** that streams artifact status changes to the dashboard via Server-Sent Events (SSE). When an artifact moves from "building" to "running", every connected browser tab sees it instantly.

## Outlook

vibeD is at the beginning of its journey. The v0.1.0 release covers the core loop to deploy, update, rollback and observe but the vision goes further:

- **OAuth / OIDC authentication** — integrate with corporate identity providers so every employee gets their own deployment space
- **Multi-tenant isolation** — namespace-per-user or namespace-per-team, with resource quotas and network policies
- **GitOps integration** — store artifact definitions in Git and reconcile with tools like Flux or Argo CD
- **Cost visibility** — show teams what their artifacts cost, and auto-cleanup idle deployments
- **Template marketplace** — pre-built starting points for common artifact types (landing pages, APIs, dashboards)

The broader bet is this: as AI tools become the primary way non-technical people create software, the deployment layer needs to become invisible. vibeD is that invisible layer, a deployment platform that AI agents talk to natively, so humans never have to.

If this resonates with you, check out the [GitHub repository](https://github.com/vibed-project/vibeD), try the `make dev` one-command setup, and let us know what you think.