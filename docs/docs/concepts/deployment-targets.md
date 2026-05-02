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

## Kubernetes (Always Available)

Plain Kubernetes deployments as a fallback:

- **Deployment + Service** with NodePort
- **Always available** on any Kubernetes cluster
- **Manual scaling** via replica count

vibeD creates a `Deployment` and a `Service` with `NodePort` type.

## Target Selection

When `target` is set to `auto` (default), vibeD picks the target in this priority:

1. **Knative** - If `serving.knative.dev` CRDs exist
2. **Sandbox** - If `agents.x-k8s.io` CRDs exist
3. **Kubernetes** - Always available as fallback

You can override this per-artifact by passing `target` to the `deploy_artifact` MCP tool.
