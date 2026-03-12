---
sidebar_position: 1
slug: /
---

# Introduction

**vibeD** is a workload orchestrator for GenAI-generated artifacts. It bridges vibe-coding tools (Claude, Gemini, ChatGPT) with your own Kubernetes infrastructure, letting AI-generated websites and web apps deploy directly to your cluster.

## Why vibeD?

Today's AI coding tools create amazing artifacts but run them in sandboxed environments. This creates problems for enterprises:

- **Data exposure** - Confidential code runs on third-party infrastructure
- **No persistence** - Artifacts disappear when the session ends
- **No integration** - Can't connect to internal services or databases

vibeD solves this by providing an **MCP server** that AI coding tools can call to deploy artifacts to **your own infrastructure**.

## How It Works

```
AI Coding Tool (Claude, Gemini, etc.)
        |
        | MCP Protocol (deploy_artifact)
        v
    ┌─────────┐
    │  vibeD   │  ← MCP Server + Dashboard
    └────┬────┘
         |
    ┌────┴────┐
    │ Build   │  ← Buildah (in-cluster Jobs)
    └────┬────┘
         |
    ┌────┴──────────────┐
    │ Deploy to:        │
    │ • Knative Serving │  ← Preferred (serverless)
    │ • Kubernetes      │  ← Fallback (always available)
    │ • wasmCloud       │  ← WebAssembly (opt-in)
    └───────────────────┘
```

## Key Features

- **MCP Server** - 7 tools for deploying, managing, and monitoring artifacts
- **Auto-detection** - Automatically picks the best deployment target
- **Buildah builder** - Auto-generates Dockerfiles, builds container images in-cluster via Kubernetes Jobs (no Docker socket required)
- **Dashboard** - Web UI showing all deployed artifacts with status and URLs
- **Multiple storage backends** - Local filesystem or GitHub repository
- **Container registry** - Push/pull images to any OCI-compatible registry
- **Helm charts** - Production-ready deployment for vibeD and its dependencies

## Quick Start

```bash
# Clone and build
git clone https://github.com/vibed-project/vibeD.git
cd vibed
make build

# Start with HTTP transport (dashboard + MCP)
./bin/vibed --config vibed.yaml --transport http
```

Then open `http://localhost:8080` to see the dashboard.
