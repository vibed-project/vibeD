---
sidebar_position: 2
---

# First Deployment

There are two ways to deploy: an AI agent via the MCP server, or the HTTP API directly. Both end up creating a `VibedApp` and returning a URL.

## Connect Claude Desktop (MCP)

vibeD speaks **HTTP streamable** MCP at `/mcp`. Bridge Claude Desktop to it with [`mcp-remote`](https://www.npmjs.com/package/mcp-remote). Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

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

Port 8080 is the host port kind's `extraPortMappings` bridges to vibeD's `/mcp` endpoint — no `kubectl port-forward` needed (see [local dev](local-dev.md) for the full bridge table). Fully quit and reopen Claude Desktop. With auth disabled (dev default) no token is needed.

Then ask Claude to deploy something:

> "Deploy a simple portfolio website with my name to vibeD."

Claude calls `deploy_artifact`; vibeD classifies the source, claims a warm sandbox, injects the source, and returns a URL once the app is `Ready`. See the [MCP tools overview](../mcp-tools/overview.md).

## Connect Goose (MCP)

[Goose](https://block.github.io/goose/) speaks MCP natively, so it connects to vibeD's `/mcp` endpoint directly as a **Streamable HTTP** extension — no `mcp-remote` bridge needed.

Interactively:

```bash
goose configure
# → Add Extension → Remote Extension (Streamable HTTP)
#   Name: vibed
#   URL:  http://localhost:8080/mcp
```

Or add it to `~/.config/goose/config.yaml` directly:

```yaml
extensions:
  vibed:
    enabled: true
    type: streamable_http
    name: vibed
    uri: http://localhost:8080/mcp
    timeout: 300
```

If you've enabled auth on the vibeD API, pass the token as a header (dev installs need none):

```yaml
    headers:
      Authorization: "Bearer <your-token>"
```

Then start a session and ask Goose to deploy — it calls the same `deploy_artifact` tool:

```bash
goose session
```

> "Deploy a static site that says hello to vibeD."

## Deploy via the HTTP API

`POST /v1/deploy` takes a multipart upload: a gzipped source tarball plus a JSON `metadata` blob.

```bash
# build a tiny static site
mkdir site && printf '<!doctype html><h1>Hello vibeD</h1>' > site/index.html
( cd site && COPYFILE_DISABLE=1 tar -czf ../site.tgz . )

curl -X POST http://localhost:8080/v1/deploy \
  -F 'source=@site.tgz;type=application/gzip' \
  -F 'metadata={"name":"hello"};type=application/json'
# → {"app_id":"hello","url":"http://6fcr8uffk2pd.localhost"}
```

The classifier picks `static-nginx` automatically. To force a lane/template, add it to metadata:

```json
{"name":"hello","runtime":{"template":"static-nginx"}}
```

A `200` means the app reached `Ready` within the latency budget and the `url` is live. A `202` with a `status_url` means it took a slow path — poll `GET /v1/apps/{app_id}` until `phase: Ready`.

:::tip Deploying Python/Node/Go
The dev install only enables the `static-nginx` warm pool. Deploying a Python or Node source without first running `make enable-python-pool` (or `enable-node-pool`) will return `Phase=Failed, Reason=TemplateMissing`. See [Local development → warm pools beyond static](local-dev.md#warm-pools-beyond-static--opt-in-per-slot).
:::

## See it

```bash
# Open the returned URL in a browser, or:
curl http://6fcr8uffk2pd.localhost/

# Or list apps
curl http://localhost:8080/v1/apps
```

`*.localhost` resolves to `127.0.0.1` in Chrome/Firefox. In Safari add an `/etc/hosts` entry for the specific label, or use Chrome. Deployed apps also appear in the dashboard at `http://localhost:8080/`.
