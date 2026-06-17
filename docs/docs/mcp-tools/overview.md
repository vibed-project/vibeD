---
sidebar_position: 1
---

# MCP Tools Overview

vibeD exposes MCP tools that an AI agent calls to deploy and manage apps. `deploy_artifact` runs the full flow: it classifies the source, creates a [`VibedApp`](../concepts/app-lifecycle.md), claims a warm sandbox, injects the source, and returns a URL.

:::note Terminology
The tool names still use "artifact" for backward compatibility, but a deploy now produces a `VibedApp` running on a sandbox — see [App lifecycle](../concepts/app-lifecycle.md). There is no separate "build" or "preview/promote" step anymore.
:::

## Available Tools

| Tool | Description |
|------|-------------|
| [`deploy_artifact`](./deploy-artifact) | Deploy a source tree; returns a URL once the app is `Ready` |
| [`list_artifacts`](./list-artifacts) | List the caller's apps |
| [`get_artifact_status`](./get-artifact-status) | Get status, phase, and URL for one app |
| [`update_artifact`](./update-artifact) | Redeploy an app with new source |
| [`delete_artifact`](./delete-artifact) | Tear an app down |
| [`get_artifact_logs`](./get-artifact-logs) | Retrieve logs |
| [`list_deployment_targets`](./list-deployment-targets) | Show available runtime templates |
| [`list_versions`](./list-versions) | List version snapshots for an app |
| [`rollback_artifact`](./rollback-artifact) | Roll back to a previous version |
| [`share_artifact`](./share-artifact) / [`unshare_artifact`](./unshare-artifact) | Grant / revoke read access |
| [`create_share_link`](./create-share-link) / [`list_share_links`](./list-share-links) / [`revoke_share_link`](./revoke-share-link) | Manage public share links |

User/department admin tools (`get_user`, `list_users`, `create_department`, `list_departments`) are also exposed when auth is enabled.

:::caution
Some endpoints behind these tools are not yet fully wired (live log streaming, redeploy, rollback/versions, and snapshot-based suspend). The deploy → list → status → delete path is the supported core.
:::

## Transport

The MCP server is exposed over **HTTP streamable** at `/mcp` (the `vibed` server's port). For Claude Desktop, bridge to it with `mcp-remote` — see [First deployment](../getting-started/first-deployment.md). stdio transport is also available via `server.transport: stdio`.
