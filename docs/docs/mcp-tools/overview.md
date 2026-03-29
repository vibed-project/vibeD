---
sidebar_position: 1
---

# MCP Tools Overview

vibeD exposes 14 MCP tools that AI coding tools can call to deploy and manage artifacts.

## Available Tools

| Tool | Description |
|------|-------------|
| [`deploy_artifact`](./deploy-artifact) | Deploy source files as a web artifact |
| [`list_artifacts`](./list-artifacts) | List deployed artifacts (paginated) |
| [`get_artifact_status`](./get-artifact-status) | Get detailed status for one artifact |
| [`update_artifact`](./update-artifact) | Update an existing artifact with new files |
| [`delete_artifact`](./delete-artifact) | Stop and remove an artifact |
| [`get_artifact_logs`](./get-artifact-logs) | Retrieve pod logs for debugging |
| [`list_deployment_targets`](./list-deployment-targets) | Show available deployment backends |
| [`list_versions`](./list-versions) | List version snapshots for an artifact |
| [`rollback_artifact`](./rollback-artifact) | Roll back to a previous version |
| [`share_artifact`](./share-artifact) | Grant read-only access to other users |
| [`unshare_artifact`](./unshare-artifact) | Revoke shared access |
| [`create_share_link`](./create-share-link) | Create a public shareable link |
| [`list_share_links`](./list-share-links) | List share links for an artifact |
| [`revoke_share_link`](./revoke-share-link) | Revoke a public share link |

## Transport Modes

vibeD supports three transport modes for MCP:

- **stdio** - Standard input/output (for CLI integration like Claude Desktop)
- **http** - Streamable HTTP endpoint at `/mcp/` (for networked access)
- **both** - Runs both simultaneously

Configure via `server.transport` in `vibed.yaml` or the `--transport` CLI flag.
