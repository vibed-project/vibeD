---
sidebar_position: 1
---

# Production Deployment

This guide walks through deploying vibeD to a production Kubernetes cluster using the Helm chart.

## Prerequisites

- **Kubernetes** 1.27+ cluster (EKS, GKE, AKS, or self-managed)
- **Helm** 3.12+
- **kubectl** configured with cluster access
- **Container registry** (ghcr.io, ECR, GCR, ACR, or Docker Hub)
- **cert-manager** (optional, for automated TLS certificates)
- **Knative Serving** (optional, for serverless deployment targets)

:::tip
For local development with Kind and Podman, see [Local Development Setup](../getting-started/local-dev.md) instead.
:::

## Step 1: Build and Push the Container Image

vibeD ships with a multi-stage Dockerfile that produces a minimal distroless image:

```bash
# Build the container image
docker build -t ghcr.io/myorg/vibed:v1.0.0 .

# Push to your registry
docker push ghcr.io/myorg/vibed:v1.0.0
```

The Dockerfile uses three stages:
1. **Node.js 22** - Builds the React dashboard frontend
2. **Go 1.23** - Compiles the Go binary with `CGO_ENABLED=0`
3. **distroless/static-debian12:nonroot** - Minimal runtime (no shell, non-root user)

The final image exposes port 8080 and runs as a non-root user.

## Step 2: Install Dependencies

vibeD can deploy artifacts to Knative or plain Kubernetes. If you want Knative (recommended for serverless scaling), install it first.

### Option A: vibed-deps Chart (Quick Start)

The bundled `vibed-deps` chart installs Knative Serving and Kourier:

```bash
helm install vibed-deps deploy/helm/vibed-deps/ \
  --set knative.domain=vibed.example.com \
  --set knative.kourier.nodePort=0
```

:::warning
The vibed-deps chart uses a Job with cluster-admin privileges. For production clusters with existing Knative installations, use Option B.
:::

### Option B: Manual Knative Installation (Recommended)

For production clusters, install Knative manually for full control. See [Knative Setup for Production](./knative-setup.md) for detailed instructions.

### Skipping Knative

Knative is optional. Without it, vibeD deploys artifacts as standard Kubernetes Deployments with NodePort Services. Set `config.deployment.preferredTarget: "kubernetes"` in your values file. See [Deployment Targets](../concepts/deployment-targets.md) for details.

## Step 3: Create Production Values File

Create a `values-production.yaml` file tailored for your environment:

```yaml
replicaCount: 1

image:
  repository: ghcr.io/myorg/vibed
  tag: "v1.0.0"
  pullPolicy: Always

resources:
  limits:
    cpu: "1"
    memory: 1Gi
  requests:
    cpu: 250m
    memory: 256Mi

persistence:
  enabled: true
  size: 50Gi
  storageClass: "gp3"     # Use your cluster's storage class
  accessModes:
    - ReadWriteOnce

config:
  server:
    transport: "http"
    httpAddr: ":8080"
    logFormat: "json"        # Structured JSON logs for log aggregation
    logLevel: "info"
  deployment:
    preferredTarget: "auto"
    namespace: "vibed-artifacts"
  builder:
    engine: "buildah"
    buildah:
      image: "quay.io/buildah/stable:latest"
      timeout: "10m"
      insecure: false            # Set true for in-cluster HTTP registries
  storage:
    backend: "local"
    local:
      basePath: "/data/vibed/artifacts"
  registry:
    enabled: true
    url: "ghcr.io/myorg/vibed-artifacts"
  store:
    backend: "sqlite"        # Default — persistent across restarts
    sqlite:
      path: "/data/vibed.db"
  knative:
    domainSuffix: "vibed.example.com"
    ingressClass: "kourier.ingress.networking.knative.dev"

metrics:
  enabled: true

auth:
  enabled: true
  mode: "apikey"
  existingSecret: "vibed-auth"
  tls:
    enabled: false           # Set to true if NOT using Ingress TLS termination
```

:::tip
For Git-based storage instead of local filesystem, see [Storage Backends](../configuration/storage.md). For full configuration reference, see [Configuration Reference](../configuration/config-reference.md).
:::

## Step 4: Create Kubernetes Secrets

### API Key Secret

```bash
kubectl create namespace vibed-system

kubectl create secret generic vibed-auth \
  --namespace vibed-system \
  --from-literal=api-key="vibed_sk_your_production_key_here"
```

### TLS Secret (if using direct TLS, not Ingress termination)

```bash
kubectl create secret tls vibed-tls \
  --namespace vibed-system \
  --cert=/path/to/tls.crt \
  --key=/path/to/tls.key
```

Then add to your values file:

```yaml
auth:
  tls:
    enabled: true
    existingSecret: "vibed-tls"
```

For automated certificate management with cert-manager, see [Authentication & HTTPS](../configuration/authentication.md#https--tls).

### Registry Credentials

If your artifact registry requires authentication, create an image pull secret and configure Docker credentials for Buildah builds:

```bash
# For pulling the vibeD image itself
kubectl create secret docker-registry vibed-registry \
  --namespace vibed-system \
  --docker-server=ghcr.io \
  --docker-username=USERNAME \
  --docker-password=TOKEN

# For Buildah Jobs to push artifact images
kubectl create secret generic docker-config \
  --namespace vibed-system \
  --from-file=config.json=$HOME/.docker/config.json
```

See [Container Registry](../configuration/registry.md) for supported registries and authentication options.

## Step 5: Deploy with Helm

```bash
helm install vibed deploy/helm/vibed/ \
  --namespace vibed-system \
  --create-namespace \
  -f values-production.yaml
```

### Verify the deployment

```bash
# Check pod status
kubectl get pods -n vibed-system

# Wait for readiness
kubectl rollout status deployment/vibed -n vibed-system

# Port-forward to verify locally
kubectl port-forward svc/vibed 8080:8080 -n vibed-system

# Test the health endpoint
curl http://localhost:8080/healthz
```

## Step 6: Expose vibeD Externally

### Option A: Ingress (Recommended)

Create an Ingress resource to expose vibeD with TLS termination:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: vibed
  namespace: vibed-system
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - vibed.example.com
      secretName: vibed-ingress-tls
  rules:
    - host: vibed.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: vibed
                port:
                  number: 8080
```

:::note
The Helm chart includes an `ingress` values block for future use, but does not yet generate an Ingress template. Create the Ingress resource manually as shown above.
:::

### Option B: LoadBalancer Service

Override the service type in your values file:

```yaml
service:
  type: LoadBalancer
  port: 8080
```

Then retrieve the external IP:

```bash
kubectl get svc vibed -n vibed-system -o jsonpath='{.status.loadBalancer.ingress[0]}'
```

## Step 7: Container Registry Access

vibeD needs push access to a container registry for Buildah build output. The Buildah Jobs push images directly to the registry.

### Cloud Workload Identity (Recommended)

For cloud-managed clusters, use workload identity to avoid managing credentials:

- **GKE**: [Workload Identity Federation](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity)
- **EKS**: [IAM Roles for Service Accounts (IRSA)](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html)
- **AKS**: [Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview)

### Docker Config Mount

Alternatively, mount a Docker config as a volume in the pod.

See [Container Registry](../configuration/registry.md) for full details.

## Scaling Considerations

vibeD defaults to a single replica. Before scaling, consider:

- **PVC Access Mode**: The default `ReadWriteOnce` PVC cannot be shared across replicas on most storage classes.
  - For multi-replica HA, use a `ReadWriteMany` storage class (NFS, EFS, Azure Files) or switch to a Git-based storage backend (GitHub/GitLab).
  - See [Storage Backends](../configuration/storage.md) for Git-backed storage options.
- **Build Resources**: Buildah build Jobs are CPU and memory intensive. Size resource limits based on expected concurrent builds.
- **Artifact Scaling**: Knative auto-scales deployed artifacts independently from vibeD itself.

## Upgrading

```bash
helm upgrade vibed deploy/helm/vibed/ \
  --namespace vibed-system \
  -f values-production.yaml
```

The Deployment template includes a `checksum/config` annotation that triggers an automatic rolling update when the ConfigMap changes. This ensures configuration changes are applied without manual pod restarts.

Verify the upgrade:

```bash
kubectl rollout status deployment/vibed -n vibed-system
```

## Security Checklist

Before going to production, verify:

- [ ] **Authentication enabled** (`auth.enabled: true`)
- [ ] **TLS enabled** (direct TLS or Ingress termination)
- [ ] **API keys in Kubernetes Secrets**, not in values files
- [ ] **Distroless non-root image** (default Dockerfile)
- [ ] **RBAC scoped** via Helm-generated ClusterRole (not cluster-admin)
- [ ] **Network policies** restrict pod-to-pod traffic
- [ ] **Registry credentials** use workload identity or image pull secrets
- [ ] **Metrics endpoint** (`/metrics`) not publicly exposed (use NetworkPolicy or firewall)
- [ ] **Store backend** set to `sqlite` (default) or `configmap` (not `memory`)
- [ ] **Knative domain** uses a real domain (not `sslip.io`)
