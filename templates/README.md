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

| Template       | Lane    | Status        |
|----------------|---------|---------------|
| `static-nginx` | fast    | landed (C2.2) |
| `node-24`      | general | pending (C2.x — migrated from `runners/node`) |
| `python-313`   | general | pending (C2.x — migrated from `runners/python`) |
| `go-123`       | general | pending |
| `base-al2023`  | general | pending |
| `workerd`      | fast    | pending (milestone E1) |
| `spin`         | fast    | pending (milestone E2) |

Every image **must** embed `vibed-agent` at `/usr/local/bin/vibed-agent`
and run it as the process the container's `ENTRYPOINT` resolves to
(supervised by `tini` for zombie reaping — the agent supervises its own
direct child only, see `internal/runneragent/agent.go`).
