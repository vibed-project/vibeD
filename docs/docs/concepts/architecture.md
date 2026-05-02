---
sidebar_position: 1
---

# Architecture

vibeD is a single Go binary that serves three concerns:

1. **MCP Server** - Protocol endpoint for AI coding tools
2. **REST API** - JSON API for the dashboard
3. **Web Dashboard** - React SPA for monitoring artifacts

## System Architecture

```
┌─────────────────────────────────────┐
│            AI Coding Tool           │
│     (Claude, Gemini, ChatGPT)       │
└──────────────┬──────────────────────┘
               │ MCP Protocol
               v
┌──────────────────────────────────────┐
│              vibeD                   │
│                                      │
│  ┌──────────┐  ┌──────────────────┐  │
│  │ MCP      │  │ REST API         │  │
│  │ Server   │  │ + Dashboard      │  │
│  └────┬─────┘  └────────┬────────┘  │
│       └───────┬──────────┘           │
│               v                      │
│       ┌──────────────┐               │
│       │ Orchestrator │               │
│       └──┬───┬───┬───┘               │
│          │   │   │                   │
│   ┌──────┘   │   └──────┐           │
│   v          v          v            │
│ ┌──────┐ ┌──────┐ ┌─────────┐       │
│ │Store │ │Build │ │Deployer │       │
│ │      │ │(ah)  │ │Factory  │       │
│ └──────┘ └──────┘ └─┬───┬──┘       │
│                      │   │           │
│              ┌───────┘   └───────┐   │
│              v                   v   │
│         ┌─────────┐      ┌─────────┐│
│         │Knative  │      │   K8s   ││
│         └─────────┘      └─────────┘│
└──────────────────────────────────────┘
```

## Key Design Principles

- **Interface-driven** - Every subsystem (Builder, Deployer, Storage, Store) is behind a Go interface with 2+ implementations
- **Single binary** - No microservices, no sidecars, one container
- **Environment detection** - Auto-discovers available deployment targets by checking CRDs
- **Fail-safe** - If Knative isn't available, falls back to plain Kubernetes

## Real-Time Events

vibeD includes an in-memory EventBus (`internal/events`) that publishes artifact lifecycle events. The orchestrator emits events on every status transition (pending → building → deploying → running, or failed/deleted), and connected clients receive them instantly via Server-Sent Events (SSE) at `GET /api/events`.

```
Orchestrator ──publish──► EventBus ──fan-out──► SSE Handler ──stream──► Dashboard
                                   └──────────► SSE Handler ──stream──► Dashboard (tab 2)
```

Key characteristics:
- **Non-blocking fan-out** — slow consumers are dropped, never block the orchestrator
- **No persistence** — events are fire-and-forget; reconnecting clients do a full data fetch
- **Auto-reconnect** — the browser's `EventSource` API reconnects automatically; the dashboard falls back to polling if SSE fails entirely

## Subsystems

| Subsystem | Interface | Implementations |
|-----------|-----------|-----------------|
| **Store** | `ArtifactStore` | In-memory, ConfigMap, SQLite (all support owner-scoped listing) |
| **Storage** | `Storage` | Local filesystem, GitHub, GitLab, UserStorageRouter (per-user routing) |
| **Builder** | `Builder` | Buildah (K8s Jobs) — runs provided Dockerfile or auto-generates one per language |
| **Deployer** | `Deployer` | Knative, Sandbox, Kubernetes |
| **Registry** | `Registry` | Any OCI-compatible registry |
| **EventBus** | — | In-memory pub/sub with SSE streaming |
