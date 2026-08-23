---
sidebar_position: 4
---

# Connect pi (pi.dev)

[pi](https://pi.dev) is a minimal, extensible terminal coding agent. It has **no built-in MCP support by design**; MCP comes from an extension. The most complete one is [`pi-mcp-adapter`](https://pi.dev/packages/pi-mcp-adapter), which connects to remote streamable-HTTP servers like vibeD and exposes their tools through a single lightweight proxy tool.

## 1. Install the adapter

```bash
pi install npm:pi-mcp-adapter
```

This registers the extension in `~/.pi/agent/settings.json`. Add `-l` to install it for the current project only (`.pi/settings.json`).

## 2. Point it at vibeD

`pi-mcp-adapter` reads a standard `mcpServers` config. Global config goes in `~/.config/mcp/mcp.json`; a project can add or override servers in `.mcp.json` (shared with other MCP clients, e.g. Claude Code) or `.pi/mcp.json` (pi-only).

Dev install (auth disabled):

```json
{
  "mcpServers": {
    "vibed": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Production install with an API key: keep the token in an environment variable, the adapter expands `${VAR}`:

```json
{
  "mcpServers": {
    "vibed": {
      "url": "https://vibed.example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${VIBED_API_KEY}"
      },
      "lifecycle": "lazy"
    }
  }
}
```

`lifecycle` controls when the adapter opens the connection: `lazy` (the default, on first tool call), `eager` (at startup), `keep-alive`, or `lazy-keep-alive`. `lazy` is fine for vibeD; deploys are infrequent and the first call adds only a few hundred milliseconds.

:::tip Same file as Claude Code
If the repo already has an `.mcp.json` for [Claude Code](./connect-claude-code.md), pi picks it up unchanged: same `mcpServers` key, same `url`/`headers` shape, same `${VAR}` expansion. One file serves both agents.
:::

## 3. Verify

Start `pi` in the project and run:

```text
/mcp            # connection status per server
/mcp tools      # tool names the adapter discovered, expect deploy_artifact, list_artifacts, …
```

If vibeD uses OIDC instead of an API key, `/mcp-auth vibed` runs the OAuth flow against the authorization server vibeD advertises at `/.well-known/oauth-protected-resource`.

## 4. Deploy

```text
> Deploy the site in ./dist to vibeD and tell me the URL.
```

The adapter routes the call to `deploy_artifact`; pi prints the returned URL once the app is `Ready`. "Show the logs", "update it with my latest changes", "delete it" map to `get_artifact_logs`, `update_artifact` and `delete_artifact`; see the [MCP tools reference](../mcp-tools/overview.md).

## Troubleshooting

| Symptom | Fix |
|---|---|
| `/mcp` shows `vibed` disconnected with 401 | Auth is on and the header is missing, or `VIBED_API_KEY` isn't exported in pi's shell |
| Connection refused | `curl http://localhost:8080/readyz`: vibeD isn't up, or the URL in the config is wrong |
| Tools listed but a deploy returns `TemplateMissing` | Enable the warm pool: [local dev → warm pools](../getting-started/local-dev.md#warm-pools) |
| Changed the config, nothing happened | `/mcp reconnect vibed` |

## Alternatives

- [`@ineersa/my-pi-mcp-adapter`](https://pi.dev/packages/@ineersa/my-pi-mcp-adapter): a simpler adapter; reads `~/.pi/agent/mcp.json` / `.pi/mcp.json` with the same `mcpServers` shape.
- [`pi-codemode-mcp`](https://github.com/mitsuhiko/pi-codemode-mcp): exposes MCP servers to pi as callable code rather than tools; experimental.
- No extension at all: pi can call vibeD's [HTTP API](../reference/http-api.md) with `curl` from its bash tool, if you'd rather not add MCP.
