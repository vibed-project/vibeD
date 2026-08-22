---
sidebar_position: 2
---

# Connect Claude Desktop

Two ways to wire Claude Desktop (and claude.ai) to vibeD. Pick by where vibeD runs:

| vibeD is… | Use |
|---|---|
| Reachable on the public internet over HTTPS | [Custom connector](#option-a-custom-connector-remote-mcp) — no local tooling |
| Only on your machine / private network (e.g. `make dev`) | [`mcp-remote` bridge](#option-b-mcp-remote-bridge-local-or-private-instances) in `claude_desktop_config.json` |

## Option A: Custom connector (remote MCP)

Claude Desktop and claude.ai can connect to a remote MCP server directly, without anything running on your laptop. This needs vibeD to be reachable from Anthropic's side, so it only works for a public, TLS-terminated install (see [Authentication & HTTPS](../configuration/authentication.md#https--tls)).

1. In Claude, open **Customize → Connectors** and click **Add custom connector** (Team/Enterprise owners: **Organization settings → Connectors → Add → Custom → Web**).
2. Name it `vibed` and enter the URL: `https://vibed.example.com/mcp`.
3. Authentication: custom connectors authenticate with **OAuth**, not a static header. Run vibeD in [OIDC mode](../configuration/authentication.md#oidc-mode) — it publishes `/.well-known/oauth-protected-resource` so the connector can discover your identity provider — and, if your IdP needs it, enter the client ID/secret under **Advanced settings**.
4. Save, then start a new chat and enable the `vibed` connector in the tools menu.

:::note API-key auth and custom connectors
A custom connector cannot send a static `Authorization: Bearer` header. If your instance uses API-key auth, use Option B instead — `mcp-remote` can inject the header.
:::

## Option B: `mcp-remote` bridge (local or private instances)

Claude Desktop's config file only launches **stdio** servers, so for a local or private vibeD you bridge the HTTP endpoint with [`mcp-remote`](https://www.npmjs.com/package/mcp-remote). This is the standard setup for a `make dev` install.

Edit the config file:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "vibed": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://localhost:8080/mcp"]
    }
  }
}
```

With auth enabled, add the token as a header. Keep the secret out of the file by referencing an environment variable:

```json
{
  "mcpServers": {
    "vibed": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote", "https://vibed.example.com/mcp",
        "--header", "Authorization:${VIBED_TOKEN}"
      ],
      "env": { "VIBED_TOKEN": "Bearer vibed_sk_your_secret_key_here" }
    }
  }
}
```

(The `Authorization:${VIBED_TOKEN}` form with no space after the colon is deliberate — it sidesteps a known argument-splitting issue in Claude Desktop.)

**Fully quit and reopen Claude Desktop** — a window close is not enough. The hammer/tools icon should now list the vibeD tools.

### Self-signed or plain-HTTP instances

- `http://localhost:8080` (dev default) works as-is; `mcp-remote` allows plain HTTP to loopback.
- For a self-signed cert (`auth.tls.autoTLS`), either trust the cert in your OS keychain or — for local testing only — set `"env": {"NODE_TLS_REJECT_UNAUTHORIZED": "0"}` on the server entry.

## Try it

> "Deploy a simple portfolio website with my name to vibeD."

Claude calls `deploy_artifact` and replies with the URL once the app is `Ready`. On a dev install the URL looks like `http://<id>.localhost/`.

## Troubleshooting

| Symptom | Check |
|---|---|
| Server missing from the tools list | Config JSON is valid (no trailing commas); Claude was fully quit; `npx -y mcp-remote http://localhost:8080/mcp` runs from a terminal |
| `401 Unauthorized` in the MCP log | Auth is on — add the `--header` (Option B) or switch to OIDC (Option A) |
| Tools appear but deploys fail with `TemplateMissing` | The needed warm pool isn't enabled; see [local dev → warm pools](../getting-started/local-dev.md#warm-pools) |
| Logs | macOS: `~/Library/Logs/Claude/mcp-server-vibed.log` |

See also [Authentication](../configuration/authentication.md) and the [MCP tools reference](../mcp-tools/overview.md).
