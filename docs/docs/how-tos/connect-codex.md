---
sidebar_position: 5
---

# Connect Codex

[Codex](https://developers.openai.com/codex) (the OpenAI Codex CLI, IDE extension and ChatGPT desktop app) speaks streamable HTTP MCP natively and shares one MCP configuration across all three surfaces. Point it at vibeD's `/mcp` endpoint and every Codex surface can deploy.

## One command

Dev install (auth disabled):

```bash
codex mcp add vibed --url http://localhost:8080/mcp
```

Production install with an API key. Codex reads the token from an environment variable rather than storing it in the config file:

```bash
export VIBED_API_KEY=vibed_sk_your_secret_key_here
codex mcp add vibed --url https://vibed.example.com/mcp --bearer-token-env-var VIBED_API_KEY
```

Verify:

```bash
codex mcp list
```

Inside a Codex session, `/mcp` shows the same status and the tools each server exposes.

## Or edit `config.toml` directly

`codex mcp add` writes to `~/.codex/config.toml`. A project can carry its own `.codex/config.toml` instead, which is the right place when a repo's apps are meant to be deployed to a shared vibeD. The equivalent entry:

```toml
[mcp_servers.vibed]
url = "https://vibed.example.com/mcp"
bearer_token_env_var = "VIBED_API_KEY"
```

`bearer_token_env_var` becomes `Authorization: Bearer <value of VIBED_API_KEY>` on every request. If you would rather set the header explicitly, or need additional headers, use `env_http_headers` (values read from the environment) or `http_headers` (literal values):

```toml
[mcp_servers.vibed]
url = "https://vibed.example.com/mcp"
env_http_headers = { "Authorization" = "VIBED_AUTH_HEADER" }   # VIBED_AUTH_HEADER="Bearer vibed_sk_..."
```

Keep secrets in the environment. A committed `.codex/config.toml` should only ever reference variable names.

### OIDC instead of an API key

If vibeD runs in [OIDC mode](../configuration/authentication.md#oidc-mode), drop the bearer settings and log in once:

```bash
codex mcp login vibed
```

Codex discovers the authorization server from vibeD's `/.well-known/oauth-protected-resource` endpoint and runs the browser flow.

## Try it

```text
> Deploy this project to vibeD and give me the URL.
```

Codex calls `deploy_artifact` and reports the URL once the app is `Ready`. Follow-ups map to the other tools: "show me the logs" calls `get_artifact_logs`, "push my changes" calls `update_artifact`, "roll back" calls `rollback_artifact`. The full list is in the [MCP tools reference](../mcp-tools/overview.md).

:::tip Make it part of the workflow
Add a line to the repo's `AGENTS.md` such as *"After UI changes, deploy with the `vibed` MCP server and report the URL"*. Codex reads `AGENTS.md` at startup and will deploy as part of its normal loop.
:::

## Troubleshooting

| Symptom | Fix |
|---|---|
| `codex mcp list` shows the server but tools fail with 401 | The token variable is not exported in the shell that launched Codex, or the key is wrong |
| Connection refused or timeout | vibeD is not reachable: `curl http://localhost:8080/readyz`; on kind, make sure `make dev` finished |
| Tools visible but the deploy returns `TemplateMissing` | Enable the warm pool: [local dev, warm pools](../getting-started/local-dev.md#warm-pools) |
| Need to remove it | `codex mcp remove vibed` |

See also [Authentication](../configuration/authentication.md) for API-key versus OIDC setup.
