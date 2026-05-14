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
# Install Knative Serving (via the testbed chart)
helm install knative testbed/knative/ \
  --namespace knative-system --create-namespace

# Install vibeD
helm install vibed deploy/helm/vibed/ \
  --set config.deployment.namespace=default
```

For the full local dev environment (Knative + Keycloak + observability stack +
agent-sandbox + an in-cluster registry), see [`testbed/kind-cluster/README.md`](https://github.com/maxkorbacher/vibed/blob/main/testbed/kind-cluster/README.md)
or run `make dev`.

## Verify Installation

```bash
# Check vibeD is running
kubectl get pods -l app.kubernetes.io/name=vibed

# Check the dashboard
kubectl port-forward svc/vibed 8080:8080
open http://localhost:8080
```
