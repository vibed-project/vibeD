---
sidebar_position: 3
---

# Connect Claude Code

[Claude Code](https://code.claude.com/docs) speaks streamable HTTP MCP natively, so it connects to vibeD's `/mcp` endpoint directly — no bridge process.

## One command

Dev install (auth disabled):

```bash
claude mcp add --transport http vibed http://localhost:8080/mcp
```

Production install with an API key:

```bash
claude mcp add --transport http vibed https://vibed.example.com/mcp \
  --header "Authorization: Bearer vibed_sk_your_secret_key_here"
```

Verify:

```bash
claude mcp list          # vibed: https://... (HTTP) - ✓ Connected
claude mcp get vibed     # shows scope, url, and any connection Issue:
```

Inside a session, `/mcp` shows the same status and the tool count.

## Choosing a scope

`claude mcp add` writes to **local** scope by default (this project, this user only). Use `--scope` to change that:

| Scope | Flag | Stored in | Use when |
|---|---|---|---|
| local | *(default)* | `~/.claude.json`, keyed to the project path | Trying it out |
| user | `--scope user` | `~/.claude.json` | You deploy to the same vibeD from every repo |
| project | `--scope project` | `.mcp.json` in the repo root, committed | The whole team should get the server |

## Share it with the team via `.mcp.json`

Project scope is the right choice for a repo whose apps are deployed to vibeD. Commit an `.mcp.json` at the repo root and keep the secret in an environment variable — Claude Code expands `${VAR}` and `${VAR:-default}` in `url` and `headers`:

```json
{
  "mcpServers": {
    "vibed": {
      "type": "http",
      "url": "${VIBED_URL:-http://localhost:8080}/mcp",
      "headers": {
        "Authorization": "Bearer ${VIBED_API_KEY}"
      }
    }
  }
}
```

Each developer then exports `VIBED_API_KEY` (and `VIBED_URL` for a shared instance) in their shell. If a variable is unset and has no default, `claude mcp list` flags it and the raw `${VIBED_API_KEY}` text is sent as-is — so an unexpected `401` usually means the variable isn't exported in the shell that launched Claude Code.

Claude Code asks for approval the first time it sees a project's `.mcp.json`; choose **Use this and all future MCP servers in this project** or approve just `vibed`.

:::note Dev install without auth
On a `make dev` instance the header is harmless but unnecessary. Either drop the `headers` block, or leave it in and let `VIBED_API_KEY` be empty — vibeD ignores the header when auth is disabled.
:::

## Try it

```text
> Deploy this project to vibeD and give me the URL.
```

Claude Code tars the working tree it decides is the app, calls `deploy_artifact`, and returns the URL. Follow-ups map to the other tools: "show me the logs" → `get_artifact_logs`, "push my changes" → `update_artifact`, "roll back" → `rollback_artifact`. Full list in the [MCP tools reference](../mcp-tools/overview.md).

:::tip Let Claude drive the whole loop
Put a line in your project's `CLAUDE.md` such as *"After making UI changes, deploy to vibeD with the `vibed` MCP server and report the URL"*, and Claude Code will deploy as part of its normal workflow.
:::

## Troubleshooting

| Symptom | Fix |
|---|---|
| `✘ Failed to connect` with `401` | Token missing/wrong, or env var not exported in the shell that launched `claude` — check `claude mcp get vibed` |
| `✘ Connection error` | vibeD not reachable: `curl http://localhost:8080/readyz`; on kind, make sure `make dev` finished |
| Warning `Leading or trailing whitespace in: headers.Authorization` | The token was pasted with a newline — re-add without it |
| Tools visible but `TemplateMissing` on deploy | Enable the warm pool: [local dev → warm pools](../getting-started/local-dev.md#warm-pools) |
| Need to remove it | `claude mcp remove vibed` (add `--scope` if it was added to user/project scope) |

See also [Authentication](../configuration/authentication.md) for API-key vs OIDC setup.
