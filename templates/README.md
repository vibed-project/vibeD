# Templates

Each subdirectory is one runtime template referenced by the classifier
(see `internal/classifier`). A template directory ships:

- `Dockerfile` — image built by CI on template changes only, **never per
  deploy** (refactor.md §1.3). Tagged and pushed to the configured
  registry.
- `entrypoint.sh` — copied into the image; invoked by `vibed-agent` after
  user source is extracted into `/workspace`.
- `template.yaml` — the `SandboxTemplate` + `SandboxWarmPool` Kubernetes
  manifests. Both target `extensions.agents.x-k8s.io/v1alpha1`
  (kubernetes-sigs/agent-sandbox v0.4.5+). The warm pool's name matches
  the template's name so the controller can derive one from the other.

The v1 set (refactor.md §6.1):

| Template       | Lane    | Pool size | Status        |
|----------------|---------|-----------|---------------|
| `static-nginx` | fast    | 30        | landed (C2.2) |
| `node-24`      | general | 50        | landed (C2.3) |
| `python-313`   | general | 50        | landed (C2.4) |
| `go-123`       | general | 20        | landed (C2.5) |
| `base-al2023`  | general | 30        | landed (C2.6) |
| `workerd`      | fast    | n/a       | pending (milestone E1) |
| `spin`         | fast    | n/a       | pending (milestone E2) |

Every image **must** embed `vibed-agent` at `/usr/local/bin/vibed-agent`
and run it as the process the container's `ENTRYPOINT` resolves to
(supervised by `tini` for zombie reaping — the agent supervises its own
direct child only, see `internal/runneragent/agent.go`).

## Building

The build context **must be the repo root** — the Dockerfiles compile
`vibed-agent` from the Go source tree:

```sh
make runner-images        # build node-24 + python-313 locally
make load-runner-images   # build + load into the Kind dev cluster
```

CI builds and pushes multi-arch images to GHCR on changes under
`templates/**`, the agent source, or `pkg/vibedapi/**` — see
`.github/workflows/runner-images.yaml`. The build artifacts in
`bin/vibed-controller` and friends are produced by separate `make` targets.

## Adding a pre-baked dependency to a language template

The manifest in `internal/prebaked/manifests/` and the install step in the
template's `Dockerfile` **must stay in sync** — vibeD's dependency gate
trusts the embedded manifest to reflect what the image actually contains.
To add a package:

1. Add it to the `pip install` / `npm install` line in the template's
   `Dockerfile`.
2. Add its normalized name to `packages:` in the matching
   `internal/prebaked/manifests/*.yaml`.
3. Rebuild and push the image.
