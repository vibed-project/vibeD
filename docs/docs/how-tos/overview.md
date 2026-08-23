---
sidebar_position: 1
slug: /how-tos
---

# How-Tos

Task-oriented guides for doing specific things with vibeD. Each one assumes you already have a running instance (locally via [`make dev`](../getting-started/local-dev.md) or a [production install](../deployment/production-guide.md)) and walks through one job end to end.

## Connect an AI agent

vibeD exposes its tools over [MCP](../mcp-tools/overview.md) at `/mcp` (streamable HTTP), so any MCP client can deploy to it. These guides cover the exact client-side config for the most common ones:

| Guide | Client | Transport |
|---|---|---|
| [Connect Claude Desktop](./connect-claude-desktop.md) | Claude Desktop / claude.ai | Remote connector, or `mcp-remote` bridge |
| [Connect Claude Code](./connect-claude-code.md) | Claude Code CLI | Native streamable HTTP (`claude mcp add`) |
| [Connect Codex](./connect-codex.md) | Codex CLI, IDE extension, ChatGPT desktop | Native streamable HTTP (`codex mcp add`) |
| [Connect pi](./connect-pi.md) | [pi.dev](https://pi.dev) coding agent | `pi-mcp-adapter` extension |

Goose is covered in [First deployment](../getting-started/first-deployment.md#connect-goose-mcp).

## Before you start: what every client needs

Whichever client you use, you need the same two things:

1. **The MCP URL.** `http://localhost:8080/mcp` on a dev install; `https://<your-host>/mcp` in production.
2. **A bearer token, if auth is on.** The dev overlay runs with auth disabled, so no token is needed. A production install uses an [API key](../configuration/authentication.md#api-key-mode-default) or [OIDC](../configuration/authentication.md#oidc-mode); the guides show where each client takes the `Authorization: Bearer …` header.

:::tip Test the endpoint first
If a client reports "failed to connect", rule out vibeD itself before debugging the client:

```bash
curl -i http://localhost:8080/readyz          # should be 200
curl -i -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

A `401` on the second call means auth is enabled and you need to pass `-H 'Authorization: Bearer <token>'`.
:::

## Once connected

Every client ends up calling the same tools. Try:

> "Deploy a single-page site that says hello from vibeD."

The agent calls [`deploy_artifact`](../mcp-tools/deploy-artifact.md), vibeD claims a warm sandbox, and the reply contains the app URL. From there, `get_artifact_status`, `get_artifact_logs`, `update_artifact` and `delete_artifact` cover the rest of the lifecycle; see the [tool reference](../mcp-tools/overview.md).
