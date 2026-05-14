---
sidebar_position: 2
---

# Deployment Targets

vibeD supports multiple deployment targets. It auto-detects which are available and picks the best one.

## Knative Serving (Preferred)

Knative provides the best experience for scalable web artifacts:

- **Automatic HTTPS** with auto-generated certificates
- **Scale-to-zero** for cost efficiency
- **Clean URLs** like `my-app.default.example.com`
- **Revision-based rollbacks**

vibeD creates Knative `Service` resources that manage revisions, routing, and scaling automatically.

## Agent Sandbox

The Agent Sandbox ([kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)) provides isolated, stateful singleton workloads:

- **VM-like experience** built on Kubernetes primitives
- **Stable identity** via a stable network hostname
- **Persistent storage** support
- **Ideal for AI Runtimes** and executing generated code in an isolated manner

vibeD creates a `Sandbox` CRD, which provisions an underlying pod and service.

## Runner — Instant Preview Fast Path

The `runner` target is vibeD's fast path for Python/Node apps: instead of
building a container image, vibeD injects the source into a **warm, pre-running
pooled pod**. There is no per-request build — eligible deploys are near-instant.

- **No container build** — source is injected over an in-cluster control API
- **Warm pool** — idle runner pods are kept ready ahead of demand
- **Ephemeral** — the deploy is a `preview`; it can be **promoted** to a durable
  built artifact (on one of the targets above) at any time
- **Dependency-gated** — only applies when every declared dependency is pre-baked
  into the runner image

It is **disabled by default** and config-driven (not auto-detected), and
requires the `agents.x-k8s.io` CRD. See
[Instant Preview](./instant-preview.md) for the full picture.

## Kubernetes (Always Available)

Plain Kubernetes deployments as a fallback:

- **Deployment + Service** with NodePort
- **Always available** on any Kubernetes cluster
- **Manual scaling** via replica count

vibeD creates a `Deployment` and a `Service` with `NodePort` type.

## Target Selection

When `target` is set to `auto` (default):

1. **Runner** — if the [Instant Preview](./instant-preview.md) fast path is
   enabled and the app is eligible (runnable language, all dependencies
   pre-baked), it takes precedence — no build, near-instant.
2. Otherwise vibeD picks a build-and-deploy target by priority:
   - **Knative** — if `serving.knative.dev` CRDs exist
   - **Sandbox** — if `agents.x-k8s.io` CRDs exist
   - **Kubernetes** — always available as fallback

You can override this per-artifact by passing `target` to the `deploy_artifact`
MCP tool. Passing `target: runner` for an app that fails the dependency gate
returns a clear error rather than silently falling back.
