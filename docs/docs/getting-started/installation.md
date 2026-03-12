---
sidebar_position: 1
---

# Installation

## Prerequisites

- **Go 1.25+** (or 1.23+ with `GOTOOLCHAIN=auto`)
- **Kubernetes cluster** (Kind, Minikube, or production)
- **Container runtime** (Docker or Podman)
- **kubectl** configured to access your cluster
- **Node.js 20+** (for frontend build)

## Build from Source

```bash
git clone https://github.com/vibed-project/vibeD.git
cd vibed

# Build frontend + backend
make build-all

# Or just the Go binary (uses pre-built frontend)
make build
```

## Install with Helm

```bash
# Install dependencies (Knative Serving)
helm install vibed-deps deploy/helm/vibed-deps/

# Install vibeD
helm install vibed deploy/helm/vibed/ \
  --set config.deployment.namespace=default
```

## Verify Installation

```bash
# Check vibeD is running
kubectl get pods -l app.kubernetes.io/name=vibed

# Check the dashboard
kubectl port-forward svc/vibed 8080:8080
open http://localhost:8080
```
