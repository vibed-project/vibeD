# Contributing to vibeD

Thanks for your interest in vibeD! Contributions of all kinds are welcome —
bug reports, documentation fixes, tests, and code. This guide covers how to
get a local development environment running and what we expect from a pull
request.

vibeD is an Apache-2.0 project. By contributing you agree that your work is
licensed under the same terms — see [LICENSE](LICENSE).

## Prerequisites

- **Go 1.23+** (with `GOTOOLCHAIN=auto`) or Go 1.25+. The module targets a
  newer Go than 1.23, so on an older toolchain you must set `GOTOOLCHAIN=auto`
  to let the `go` command fetch the required version on demand.
- **`GO111MODULE=on`** — required if your environment disables modules by
  default.
- **kubectl** configured against a cluster (Kind is used for local dev).
- A **container runtime** (Podman or Docker) for building images and running
  the Kind-based dev loop.
- **Node.js / npm** if you plan to work on the React dashboard (`web/`) or the
  documentation site (`docs/`).

The `make` targets already export `GOTOOLCHAIN=auto GO111MODULE=on` for every
`go` invocation, so you rarely need to set them by hand. If you run `go`
directly, prefix it yourself:

```bash
GOTOOLCHAIN=auto GO111MODULE=on go build ./...
```

## Building

```bash
make build          # Build the vibed control-plane binary (bin/vibed)
make build-all      # Build the React dashboard, then the vibed binary
```

vibeD ships six binaries. The remaining ones have their own targets:

```bash
make build-controller       # bin/vibed-controller (VibedApp reconciler)
make build-router           # bin/vibed-router (programs Caddy routes)
make build-workerd-loader   # bin/vibed-workerd-loader (fast-lane loader)
```

To confirm the whole module compiles:

```bash
GOTOOLCHAIN=auto GO111MODULE=on go build ./...
```

## Testing

```bash
make test               # Unit tests (go test ./...)
make e2e                # In-process end-to-end tests, no cluster required
make test-integration   # Integration tests (build tag: integration)
```

Notes:

- `make e2e` runs the in-process end-to-end slice and does **not** need a
  cluster.
- `make e2e-cluster` runs the cluster smoke test and is skipped unless a Kind
  cluster with the chart installed is present. See `test/e2e/README.md`.
- `make test-integration` loads a couple of test images into the Kind cluster
  first (via `test-integration-setup`), so a running dev cluster is expected.
  Use `make test-integration-short` for the shorter variant, and
  `make test-cleanup` to remove test namespaces afterward.

Please add or update tests alongside any behavior change.

## Code style

Format and vet your code before opening a pull request:

```bash
gofmt -w .
GOTOOLCHAIN=auto GO111MODULE=on go vet ./...
make lint           # golangci-lint plus the import-boundary check
```

`make lint` also runs `make boundary`, which asserts the module stays
self-contained. Generated code (CRD YAML in `crds/`, DeepCopy methods, and the
OpenAPI server stubs in `pkg/vibedapi/http/`) is committed — do not hand-edit
it. If you change the `VibedApp` API or `api/openapi.yaml`, regenerate instead:

```bash
make generate       # DeepCopy methods
make manifests      # CRD YAML
make openapi-gen    # HTTP types + server stubs
```

## Local development loop

`make dev` stands up a complete local environment: it bootstraps a Kind
cluster, installs agent-sandbox and the testbed dependencies, builds and loads
the vibed / controller / router / static-nginx images, and helm-installs vibeD
with the dev overlay.

```bash
make dev            # Full local setup (Kind + agent-sandbox + build + install)
make dev-status     # Print pod status and the local service URLs
make teardown       # Delete the Kind cluster
```

When `make dev` finishes, the dashboard is at `http://localhost:8080/` and
deployed apps are reachable at `http://<id>.localhost/` — Kind's port mappings
bridge the host ports, so no `kubectl port-forward` is needed. The dev install
enables only the `static-nginx` warm pool; enable the others with
`make enable-python-pool` or `make enable-node-pool`.

## Documentation

The documentation lives in `docs/` as a Docusaurus site.

```bash
make docs-install   # Install documentation dependencies
make docs-dev       # Start the docs dev server (hot reload)
make docs-build     # Production build of the docs site
```

Documentation-only changes are very welcome and are a great first contribution.

Every pull request that touches `docs/` gets a **Docs preview** check: CI runs
the production build (which fails on broken links or anchors) and attaches the
built site as a downloadable artifact, linked from a bot comment on the PR.
Unzip it and `npx -y serve <dir>` to review the rendered pages locally. The live
site at vibed.run is only redeployed on merge to `main`.

## Extensibility

vibeD is built to be extended out of tree. The state store, source storage
backends, and other integration points are pluggable, and there is an
editions/feature-flag seam whose default enables **all** features. Operators
and integrators can implement these interfaces in their own out-of-tree Go
module without forking vibeD. The `make boundary` check exists precisely to
keep this repository self-contained so those external modules have a stable
surface to build against.

## Submitting changes

1. Fork the repository and create a topic branch off `main`.
2. Make your change, add tests, and run `make test` and `make lint` locally.
3. Keep commits focused and write clear commit messages.
4. Open a pull request describing what changed and why.

### Developer Certificate of Origin

All commits must be signed off under the
[Developer Certificate of Origin](DCO). This is a lightweight statement that
you wrote the patch or otherwise have the right to submit it under the
project's open-source license. Add the sign-off automatically with the `-s`
flag:

```bash
git commit -s -m "Your commit message"
```

This appends a `Signed-off-by: Your Name <your@email>` trailer to the commit
message. Every commit in a pull request needs one; if you forgot, amend with
`git commit --amend -s` (or rebase to sign off a series). The name and email
in the trailer must match the commit author.

## Getting help

If you are unsure where to start, open an issue describing what you would like
to work on. We are happy to point you in the right direction.
