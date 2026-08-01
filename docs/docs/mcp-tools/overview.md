---
sidebar_position: 1
---

# MCP Tools Overview

vibeD exposes MCP tools that an AI agent calls to deploy and manage apps. `deploy_artifact` runs the full flow: it classifies the source, creates a [`VibedApp`](../concepts/app-lifecycle.md), claims a warm sandbox, injects the source, and returns a URL.

:::note Terminology
The tool names still use "artifact" for backward compatibility, but a deploy now produces a `VibedApp` running on a sandbox — see [App lifecycle](../concepts/app-lifecycle.md). There is no separate "build" or "preview/promote" step anymore.
:::

## Available Tools

These tools are always registered.

| Tool | Description |
|------|-------------|
| [`deploy_artifact`](./deploy-artifact) | Deploy a source tree; returns an `artifact_id` and, once `Ready`, a URL |
| [`list_artifacts`](./list-artifacts) | List the caller's apps, with optional status filter and paging |
| [`get_artifact_status`](./get-artifact-status) | Get status, lifecycle phase, and URL for one app |
| [`update_artifact`](./update-artifact) | Re-inject new source into an existing app (full file replacement) |
| [`delete_artifact`](./delete-artifact) | Tear an app down and remove its stored source |
| [`get_artifact_logs`](./get-artifact-logs) | Retrieve recent log lines |
| [`list_versions`](./list-versions) | List version snapshots for an app |
| [`rollback_artifact`](./rollback-artifact) | Redeploy an app from a previous version snapshot (a new version is recorded) |
| [`create_share_link`](./create-share-link) | Mint a public link (optional password / expiry) for account-less viewers |
| [`list_share_links`](./list-share-links) | List an app's share links |
| [`revoke_share_link`](./revoke-share-link) | Invalidate a share link by token |

:::note Removed in v0.6
`list_deployment_targets` (superseded by [lanes and templates](../concepts/lanes-and-templates.md)) and the user-grant `share_artifact` / `unshare_artifact` tools were removed in v0.6. Public share links replace user-grant sharing — see [Migrating to v0.6](../migrating-to-v0.6.md).
:::

## Admin Tools

When a **user store** is configured, vibeD also registers user- and department-administration tools. These are absent when no user store is wired.

| Tool | Description | Access |
|------|-------------|--------|
| [`list_users`](./user-management#list_users) | List users, optionally filtered by `department_id` | Admin role |
| [`get_user`](./user-management#get_user) | Fetch one user by `user_id` | Admin, or the caller viewing themselves |
| [`list_departments`](./user-management#list_departments) | List all departments | Admin role |
| [`create_department`](./user-management#create_department) | Create a department by `name` | Admin role |

See [User & department management](./user-management) for input schemas, responses, and the role checks each tool enforces.

## Ownership & access control

The lifecycle tools are scoped to the calling identity: `list_artifacts` returns only the caller's apps, and `get_*` / `update_*` / `delete_*` resolve an app the caller owns. See [Authentication](../configuration/authentication.md) for how the caller identity is established.

:::note Backend routing
All lifecycle and share-link tools run against the live `VibedApp` deploy path (the same one the `/v1` API serves). The legacy orchestrator was removed in v0.6 — see [Migrating to v0.6](../migrating-to-v0.6.md).
:::

## Transport

The MCP server is exposed over **HTTP streamable** at `/mcp` (the `vibed` server's port). For Claude Desktop, bridge to it with `mcp-remote` — see [First deployment](../getting-started/first-deployment.md). stdio transport is also available via `server.transport: stdio`.
