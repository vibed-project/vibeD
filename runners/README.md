# Runner images

Generic language images for vibeD's **Instant Preview** fast path. A pooled pod
from one of these images sits idle on the cluster until vibeD injects a user's
source over the runner agent's control API — at which point the user process
starts with **no per-request container build**. See `../FAST-PATH.md`.

These images are built **once** and pushed to the cluster registry — never per
request.

## Layout

```
runners/
  python/
    Dockerfile        # python:3.12-slim + pre-baked deps + runner agent
    prebaked.yaml     # dependency manifest the fast-path gate reads
  node/
    Dockerfile        # node:22-bookworm-slim + pre-baked deps + runner agent
    prebaked.yaml
```

Each image bundles three things:

1. **A language runtime** — the interpreter, nothing app-specific.
2. **Pre-baked dependencies** — a curated set of common frameworks so that
   zero-dependency and pre-baked apps run with no install step. Anything an app
   needs that *isn't* pre-baked makes it fall back to the full container build.
3. **The runner agent** (`cmd/vibed-runner-agent`) as the entrypoint, under
   `tini` for zombie reaping. It exposes the in-cluster control API on `:9000`;
   the user app serves on `:8080`.

## Building

The build context **must be the repo root** — the Dockerfiles compile the
runner agent from the Go source tree:

```sh
make runner-images        # build both images locally
make load-runner-images   # build + load into the Kind dev cluster
```

CI builds and pushes multi-arch images to GHCR on changes to `runners/**` or
the agent source — see `.github/workflows/runner-images.yaml`.

## Adding a pre-baked dependency

The `prebaked.yaml` manifest and the install step in the `Dockerfile` **must
stay in sync** — the dependency gate trusts the manifest to reflect what the
image actually contains. To add a package:

1. Add it to the `pip install` / `npm install` line in the `Dockerfile`.
2. Add its normalized name to `packages:` in `prebaked.yaml`.
3. Rebuild and push the image.
