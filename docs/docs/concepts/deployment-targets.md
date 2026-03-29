---
sidebar_position: 2
---

# Deployment Targets

vibeD supports two deployment targets. It auto-detects which are available and picks the best one.

## Knative Serving (Preferred)

Knative provides the best experience for web artifacts:

- **Automatic HTTPS** with auto-generated certificates
- **Scale-to-zero** for cost efficiency
- **Clean URLs** like `my-app.default.example.com`
- **Revision-based rollbacks**

vibeD creates Knative `Service` resources that manage revisions, routing, and scaling automatically.

## Kubernetes (Always Available)

Plain Kubernetes deployments as a fallback:

- **Deployment + Service** with NodePort
- **Always available** on any Kubernetes cluster
- **Manual scaling** via replica count

vibeD creates a `Deployment` and a `Service` with `NodePort` type.

## Target Selection

When `target` is set to `auto` (default), vibeD picks the target in this priority:

1. **Knative** - If `serving.knative.dev` CRDs exist
2. **Kubernetes** - Always available as fallback

You can override this per-artifact by passing `target` to the `deploy_artifact` MCP tool.
