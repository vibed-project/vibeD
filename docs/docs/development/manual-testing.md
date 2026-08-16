---
sidebar_position: 2
---

# Manual Test Runbook

A hands-on checklist to verify a build end-to-end: deploy one app per **runtime**, then flip the main **config** axes and re-check. It complements the [Testing Guide](testing.md) (which maps features to automated coverage) — use this before cutting a release, when bringing up a new environment, or to reproduce a runtime/config bug.

Everything here targets a local [`make dev`](../getting-started/local-dev.md) cluster and the `POST /v1/deploy` HTTP path. The MCP `deploy_artifact` tool drives the exact same code, so any case can be run through an agent instead — see [First deployment](../getting-started/first-deployment.md).

## 0. Prerequisites

A running dev cluster and the host port bridge (`8080` → vibeD):

```bash
make dev                                   # setup-cluster + deps + observability + vibeD
kubectl get pods -n vibed-system           # all Running/Ready
curl -s http://localhost:8080/v1/apps      # → {"items":[]}  (empty list is fine)
```

Paste this helper once — every case below uses it to tar a source dir and deploy it:

```bash
mkdir -p ~/vibed-test && cd ~/vibed-test
deploy() {  # deploy <name> <dir>
  ( cd "$2" && COPYFILE_DISABLE=1 tar -czf "/tmp/$1.tgz" . )
  curl -sS -X POST http://localhost:8080/v1/deploy \
    -F "source=@/tmp/$1.tgz;type=application/gzip" \
    -F "metadata={\"name\":\"$1\"};type=application/json"
  echo
}
```

A `200` response means the app reached `Ready` within the latency budget and its `url` is live. A `202` with a `status_url` took a slow path — poll `GET /v1/apps/{app_id}` until `phase: Ready`.

:::note App port contract
General-lane apps (Node/Python/Go/base) **must listen on `0.0.0.0:8080`** — that's the template's `http` port the router probes and routes to (the control API owns `:9000`). Fast-lane apps don't listen: `static-nginx` serves `/workspace`, and `workerd` runs your `fetch` handler.
:::

## 1. Runtime matrix

The classifier reads only file *names* and `package.json` top-level keys, first rule wins. What triggers each template, and how it boots:

| Source at root | Template | Lane | Warm pool | Boots by running |
| --- | --- | --- | --- | --- |
| only `.html`/`.css`/`.js`/images | `static-nginx` | fast | on by default | nginx serves `/workspace` |
| `package.json` + browser deps + `build` script | `static-nginx` | fast | on by default | builds async, serves output |
| `worker.js` / `worker.ts` / `wrangler.toml` | `workerd` | fast | `workerd.enabled=true` | your `export default { fetch }` |
| `package.json` (any other) | `node-24` | general | on by default | `npm start`, else `index/server/app/main.js` |
| `requirements.txt` / `pyproject.toml` | `python-313` | general | on by default | `app.py` / `main.py` / `server.py` / `run.py` |
| `go.mod` | `go-123` | general | `make enable-go-pool` | `./app` binary, else `go run .` |
| `Dockerfile` (hint, not built) | `base-al2023` | general | `make enable-base-pool` | kitchen-sink autodetect |
| anything else | `base-al2023` | general | `make enable-base-pool` | kitchen-sink autodetect |

:::tip Static, JS and Python work out of the box
`make install-vibed` (and `make dev`) bring up the `static-nginx`, `node-24` and
`python-313` pools, so those deploy with no extra step. The general lane runs as
normal pods on Kind — `runtime.defaultClass` is empty, so no Kata/nested virt is
needed.

The heavier `go-123` and `base-al2023` pools stay opt-in. Deploying a source that
needs a pool you haven't enabled returns `Phase=Failed, Reason=TemplateMissing`
with the explanation in the app's `message` field — **that message is the only
diagnosis, because an app that never gets a pod has no logs**. Enable the pool
(below), wait ~10s for the controller to re-validate, then redeploy.
:::

### 1a. static-nginx — fast, always on

```bash
mkdir -p static && printf '<!doctype html><h1>hello static</h1>' > static/index.html
deploy hello-static static
```

Expect `200` + a `url`. Confirm it serves:

```bash
curl http://localhost:8080/v1/apps/hello-static     # phase: Ready, a url
curl http://<label>.localhost/                      # → the HTML (label from the url field)
```

`*.localhost` resolves to `127.0.0.1` in Chrome/Firefox; in Safari add an `/etc/hosts` entry or use Chrome.

### 1b. node-24 — general

```bash
# node-24 is on by default (make install-vibed); `make enable-node-pool`
# re-enables it if you turned it off.
mkdir -p node
cat > node/package.json <<'JSON'
{ "name": "hello-node", "version": "1.0.0", "scripts": { "start": "node server.js" } }
JSON
cat > node/server.js <<'JS'
require('http').createServer((_, res) => res.end('hello from node\n')).listen(8080, '0.0.0.0');
JS
deploy hello-node node
curl http://<label>.localhost/                       # → hello from node
```

### 1c. python-313 — general

`requirements.txt` (even empty) is what routes to Python; `app.py` is the entrypoint.

```bash
# python-313 is on by default (make install-vibed); `make enable-python-pool`
# re-enables it if you turned it off.
mkdir -p py && : > py/requirements.txt
cat > py/app.py <<'PY'
from http.server import HTTPServer, BaseHTTPRequestHandler
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers()
        self.wfile.write(b"hello from python\n")
HTTPServer(("0.0.0.0", 8080), H).serve_forever()
PY
deploy hello-python py
curl http://<label>.localhost/                       # → hello from python
```

### 1d. go-123 — general

```bash
make enable-go-pool
mkdir -p goapp
cat > goapp/go.mod <<'MOD'
module hello

go 1.23
MOD
cat > goapp/main.go <<'GO'
package main

import "net/http"

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from go\n"))
	})
	http.ListenAndServe("0.0.0.0:8080", nil)
}
GO
deploy hello-go goapp
curl http://<label>.localhost/                       # → hello from go
```

The template runs `go run .` on first request-free boot, so the first deploy may take the `202` slow path — poll until `Ready`.

### 1e. base-al2023 — general (fallback)

A root `Dockerfile` routes here (it's a start-command *hint*, never built). The kitchen-sink entrypoint then autodetects `app.py`:

```bash
make enable-base-pool
mkdir -p base
printf 'FROM amazonlinux:2023\nCMD ["python3","app.py"]\n' > base/Dockerfile
cat > base/app.py <<'PY'
from http.server import HTTPServer, BaseHTTPRequestHandler
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers()
        self.wfile.write(b"hello from base\n")
HTTPServer(("0.0.0.0", 8080), H).serve_forever()
PY
deploy hello-base base
curl http://<label>.localhost/                       # → hello from base
```

### 1f. workerd — fast (optional)

Off by default (`workerd.enabled=false` in `values-kind.yaml`). Enable it, then deploy a worker:

```bash
helm upgrade --install vibed deploy/helm/vibed/ -n vibed-system \
  -f deploy/helm/vibed/values-kind.yaml --reuse-values --set workerd.enabled=true --wait
mkdir -p wk
cat > wk/worker.js <<'JS'
export default { fetch() { return new Response("hello from workerd\n"); } };
JS
deploy hello-workerd wk
curl http://<label>.localhost/                       # → hello from workerd
```

### 1g. Overrides

Skip the classifier by setting `runtime.lane` / `runtime.template`, and force the start command with `runtime.entrypoint` (it sets `VIBED_USER_COMMAND`). Handy for testing a template against a source it wouldn't normally pick:

```bash
curl -sS -X POST http://localhost:8080/v1/deploy \
  -F "source=@/tmp/hello-base.tgz;type=application/gzip" \
  -F 'metadata={"name":"forced","runtime":{"template":"base-al2023","entrypoint":"python3 app.py"}};type=application/json'
```

## 2. Config matrix

### 2a. Metadata store — `store.backend`

App metadata (artifact records, version history, users, share links, audit log) lives in the metadata store, separate from the source-blob store. Only `sqlite` implements the full surface:

| Backend | Artifacts / versions | Users | Share links | Notes |
| --- | --- | --- | --- | --- |
| `sqlite` (default) | ✅ | ✅ | ✅ | Durable on the control-plane PVC. Run the full pass here. |
| `configmap` | ✅ | — | — | State in a ConfigMap; user/share-link routes are unavailable. |
| `memory` | ✅ | — | — | Ephemeral; wiped on controller restart. Smoke tests only. |

Switch and re-verify:

```bash
helm upgrade --install vibed deploy/helm/vibed/ -n vibed-system \
  -f deploy/helm/vibed/values-kind.yaml --reuse-values --set config.store.backend=configmap --wait
kubectl rollout restart -n vibed-system deploy/vibed-controller
```

On `configmap`/`memory`, confirm deploys still work and the user/share-link endpoints **degrade gracefully** (not `500`). On `sqlite`, run the full §3 dashboard pass.

### 2b. Source-blob store — `storage.tarball.backend`

Where the deploy tarball is kept for `vibed-agent` to pull:

| Backend | Reaches the sandbox via | Use when |
| --- | --- | --- |
| `served` (default) | in-cluster PVC URL over cluster DNS | dev only — sandboxes run `Unmanaged` with normal DNS |
| `s3` | pre-signed URL over public egress | production — the only backend that survives a locked-down sandbox NetworkPolicy |

`served` is correct for `make dev`. To exercise `s3` locally, point it at MinIO (see [Storage](../configuration/storage.md)) and redeploy any runtime from §1 — the app should still reach `Ready`, now pulling from a pre-signed URL. Picking `served` under a restrictive NetworkPolicy is the classic failure: the agent can't resolve cluster DNS and the app never leaves `Pending`.

## 3. Dashboard regression pass

Open `http://localhost:8080/` and confirm the `/v1` data-plane surfaces populate — each of these regressed when the dashboard still called legacy artifact endpoints with `/v1` app IDs:

- **Metrics** — the Deploy Success Rate, Active Artifacts, and MCP Tool Usage panels show data (dashboard + Grafana) after a few deploys.
- **Live logs** — open an app → **Logs** streams (not "not yet available").
- **Versions + rollback** — redeploy one app twice → **Versions** lists `v1`/`v2` → **Rollback** to `v1` restores that source.
- **Share links** — create a password-protected link → open it in a private window → it prompts for the password → resolves to the app URL → **Revoke** → the link then 404s.

## 4. Failure modes

Expected errors — trigger each once and confirm the message, since these are the first-line diagnostics:

| Symptom | Cause | Fix |
| --- | --- | --- |
| `Phase=Failed, Reason=TemplateMissing` | general-lane pool not enabled | `make enable-<runtime>-pool`, wait ~10s, redeploy |
| `202` + `status_url` that stays `Pending` | slow path (first general-lane boot, template build) | poll `GET /v1/apps/{id}`; check pod logs |
| `no <lang> entrypoint found under /workspace` | autodetect found no known entry file | add `app.py`/`index.js`/`main.go`, or set `runtime.entrypoint` |
| app `Pending` forever on `served` + NetworkPolicy | sandbox can't reach the in-cluster PVC URL | switch `storage.tarball.backend` to `s3` |
| `*.localhost` won't open | Safari doesn't resolve `*.localhost` | use Chrome/Firefox or add an `/etc/hosts` entry |
| `413` on deploy | tarball over the 50 MB limit | shrink the source tree |

## 5. Teardown

```bash
for a in hello-static hello-node hello-python hello-go hello-base hello-workerd; do
  curl -s -X DELETE "http://localhost:8080/v1/apps/$a" >/dev/null
done
make test-cleanup          # remove test namespaces
```

## Checklist

Copy into a release ticket and tick as you go:

```
Runtimes
[ ] static-nginx  deploy Ready · URL serves · delete
[ ] node-24       pool on · deploy Ready · URL serves · delete
[ ] python-313    pool on · deploy Ready · URL serves · delete
[ ] go-123        pool on · deploy Ready · URL serves · delete
[ ] base-al2023   pool on · deploy Ready · URL serves · delete
[ ] workerd       enabled · deploy Ready · URL serves · delete   (optional)
[ ] override      runtime.template + entrypoint honored

Config
[ ] store.backend=sqlite     full feature pass (users + share links + versions)
[ ] store.backend=configmap  deploys OK · user/share-link routes degrade, no 500
[ ] store.backend=memory     deploys OK · state cleared on restart
[ ] tarball.backend=served   dev deploys reach Ready
[ ] tarball.backend=s3       deploys reach Ready via pre-signed URL   (optional)

Dashboard
[ ] metrics panels populate
[ ] live logs stream
[ ] versions list + rollback
[ ] share link create / resolve / revoke
```
