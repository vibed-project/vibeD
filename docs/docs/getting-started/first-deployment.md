---
sidebar_position: 3
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
      "args": ["-y", "mcp-remote", "http://localhost:18090/mcp"]
    }
  }
}
```

This assumes the API port-forward from [local dev](local-dev.md) (`18090`). Fully quit and reopen Claude Desktop. With auth disabled (dev default) no token is needed.

Then ask Claude to deploy something:

> "Deploy a simple portfolio website with my name to vibeD."

Claude calls `deploy_artifact`; vibeD classifies the source, claims a warm sandbox, injects the source, and returns a URL once the app is `Ready`. See the [MCP tools overview](../mcp-tools/overview.md).

## Deploy via the HTTP API

`POST /v1/deploy` takes a multipart upload: a gzipped source tarball plus a JSON `metadata` blob.

```bash
# build a tiny static site
mkdir site && printf '<!doctype html><h1>Hello vibeD</h1>' > site/index.html
( cd site && COPYFILE_DISABLE=1 tar -czf ../site.tgz . )

curl -X POST http://localhost:18090/v1/deploy \
  -F 'source=@site.tgz;type=application/gzip' \
  -F 'metadata={"name":"hello"};type=application/json'
# → {"app_id":"hello","url":"http://6fcr8uffk2pd.localhost:18080"}
```

The classifier picks `static-nginx` automatically. To force a lane/template, add it to metadata:

```json
{"name":"hello","runtime":{"template":"static-nginx"}}
```

A `200` means the app reached `Ready` within the latency budget and the `url` is live. A `202` with a `status_url` means it took a slow path — poll `GET /v1/apps/{app_id}` until `phase: Ready`.

## See it

```bash
# Open the returned URL (dev: http://<label>.localhost:18080)
curl -H "Host: 6fcr8uffk2pd.localhost" http://localhost:18080/

# Or list apps
curl http://localhost:18090/v1/apps
```

Deployed apps also appear in the dashboard at `http://localhost:18090/`.
