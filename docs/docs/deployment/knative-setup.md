---
sidebar_position: 2
---

# Knative Setup for Production

This guide covers installing and configuring Knative Serving for production use with vibeD.

## Why Knative?

Knative Serving gives vibeD-deployed artifacts serverless capabilities:

- **Scale to zero** - Idle artifacts consume no compute resources
- **Automatic scaling** - Handles traffic spikes without manual configuration
- **Revision management** - Each deployment creates a new revision with instant rollback
- **Clean URLs** - Artifacts get DNS-friendly URLs like `my-app.default.vibed.example.com`

For a comparison of all deployment targets, see [Deployment Targets](../concepts/deployment-targets.md).

## Option A: vibed-deps Helm Chart

The bundled `vibed-deps` chart provides a quick Knative installation:

```bash
helm install vibed-deps deploy/helm/vibed-deps/ \
  --set knative.domain=vibed.example.com
```

### Production Overrides

| Value | Default | Production |
|-------|---------|------------|
| `knative.domain` | `127.0.0.1.sslip.io` | Your real domain |
| `knative.kourier.nodePort` | `31080` | `0` (use LoadBalancer) |
| `knative.version` | `v1.17.0` | Latest stable |

### Limitations

- Uses a Kubernetes Job with cluster-admin privileges
- May conflict with existing Knative installations
- Limited configuration options compared to manual install

Best for: Greenfield clusters without existing Knative or service mesh.

## Option B: Manual Installation (Recommended)

For production clusters, install Knative manually for full control over versions and configuration.

### 1. Install Knative Serving CRDs

```bash
kubectl apply -f https://github.com/knative/serving/releases/download/knative-v1.17.0/serving-crds.yaml
```

### 2. Install Knative Serving Core

```bash
kubectl apply -f https://github.com/knative/serving/releases/download/knative-v1.17.0/serving-core.yaml
```

### 3. Wait for Core Components

```bash
kubectl wait --for=condition=Available deployment --all \
  -n knative-serving --timeout=300s
```

### 4. Install Networking Layer

Choose one networking layer:

**Kourier** (lightweight, recommended for vibeD):

```bash
kubectl apply -f https://github.com/knative/net-kourier/releases/download/knative-v1.17.0/kourier.yaml

kubectl patch configmap/config-network \
  --namespace knative-serving \
  --type merge \
  --patch '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'
```

**Istio** (if you already have Istio):

```bash
kubectl apply -f https://github.com/knative/net-istio/releases/download/knative-v1.17.0/net-istio.yaml
```

**Contour** (if you already have Contour):

```bash
kubectl apply -f https://github.com/knative/net-contour/releases/download/knative-v1.17.0/net-contour.yaml
```

### 5. Configure Domain

Replace the default `sslip.io` domain with your production domain:

```bash
kubectl patch configmap/config-domain \
  --namespace knative-serving \
  --type merge \
  --patch '{"data":{"vibed.example.com":""}}'
```

## Production DNS

Deployed artifacts are accessible at `{name}.{namespace}.{domain}`. You need DNS records pointing to your networking layer's external IP.

### Find the External IP

For Kourier:

```bash
kubectl get svc kourier -n kourier-system \
  -o jsonpath='{.status.loadBalancer.ingress[0]}'
```

### Wildcard DNS Record

Create a wildcard DNS record pointing to the load balancer:

```
*.vibed.example.com  →  <LOAD_BALANCER_IP>
```

This routes all artifact URLs (e.g., `my-app.default.vibed.example.com`) to Knative.

### ExternalDNS (Alternative)

For automatic DNS management, use [ExternalDNS](https://github.com/kubernetes-sigs/external-dns) with your DNS provider. ExternalDNS watches Knative Services and creates DNS records automatically.

## Knative Auto-TLS

Knative can automatically provision TLS certificates for deployed artifacts using cert-manager.

### Prerequisites

Install cert-manager and create a ClusterIssuer:

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@example.com
    privateKeySecretRef:
      name: letsencrypt-prod
    solvers:
      - http01:
          ingress:
            class: kourier.ingress.networking.knative.dev
```

### Enable Auto-TLS

```bash
kubectl apply -f https://github.com/knative/net-certmanager/releases/download/knative-v1.17.0/release.yaml

kubectl patch configmap/config-network \
  --namespace knative-serving \
  --type merge \
  --patch '{"data":{"auto-tls":"Enabled","http-protocol":"Redirected"}}'

kubectl patch configmap/config-certmanager \
  --namespace knative-serving \
  --type merge \
  --patch '{"data":{"issuerRef":"kind: ClusterIssuer\nname: letsencrypt-prod"}}'
```

Deployed artifacts will automatically get HTTPS URLs with Let's Encrypt certificates.

## Configuring vibeD for Knative

Set these values in your Helm values file to match your Knative configuration:

```yaml
config:
  knative:
    domainSuffix: "vibed.example.com"       # Must match config-domain
    ingressClass: "kourier.ingress.networking.knative.dev"
  deployment:
    preferredTarget: "auto"                  # Auto-detects Knative via CRD check
```

vibeD detects Knative by checking for the `serving.knative.dev` CRD at startup. If found, it uses Knative as the preferred deployment target.

## Verifying Knative

```bash
# Check Knative components
kubectl get pods -n knative-serving

# Check networking layer
kubectl get pods -n kourier-system

# Deploy a test artifact via vibeD, then check
kubectl get ksvc -A

# Verify DNS resolution
nslookup my-app.default.vibed.example.com
```

## Skipping Knative

vibeD works without Knative by deploying artifacts as standard Kubernetes Deployments with NodePort or ClusterIP Services.

To explicitly skip Knative even if it is installed:

```yaml
config:
  deployment:
    preferredTarget: "kubernetes"
```

See [Deployment Targets](../concepts/deployment-targets.md) for the full comparison.
