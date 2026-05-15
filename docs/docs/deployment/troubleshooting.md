---
sidebar_position: 4
---

# Troubleshooting

Common issues and their solutions when running vibeD in production.

## vibeD Pod Not Starting

### CrashLoopBackOff

Check the pod logs for the root cause:

```bash
kubectl logs -n vibed-system deploy/vibed
```

**Common causes:**

| Symptom | Cause | Fix |
|---------|-------|-----|
| `failed to load config` | Invalid `vibed.yaml` | Check YAML syntax in ConfigMap |
| `failed to connect to kubernetes` | RBAC issue | Verify ServiceAccount and ClusterRoleBinding exist |
| `address already in use` | Port conflict | Ensure nothing else is on port 8080 |
| `unknown storage backend` | Typo in config | Valid backends: `local`, `github`, `gitlab` |

### Readiness Probe Failing

The `/readyz` endpoint returns 503 when a component is unhealthy:

```bash
# Check readiness response
kubectl port-forward -n vibed-system deploy/vibed 8080:8080
curl -s http://localhost:8080/readyz | jq
```

The response includes per-component status. Fix the failing component:

| Component | Common Cause | Fix |
|-----------|-------------|-----|
| `store` | ConfigMap not accessible | Check RBAC for ConfigMap read/write |
| `kubernetes` | API server unreachable | Check ServiceAccount token mounting |

## Builds Failing

### Builder Image Pull Failure

**Symptom:** Build Job hangs or fails with timeout pulling `quay.io/buildah/stable:latest`.

**Fixes:**
- Ensure the cluster has internet access or pre-pull the Buildah image
- Use a registry mirror: set `config.builder.buildah.image` to a mirrored copy
- If behind a proxy, configure the container runtime's proxy settings
- For Kind clusters: `docker pull quay.io/buildah/stable:latest && kind load docker-image quay.io/buildah/stable:latest`

### Registry Push Authentication Failure

**Symptom:** Build succeeds but push fails with `UNAUTHORIZED` or `denied`.

**Fixes:**
- Verify `config.registry.enabled: true` and `config.registry.url` are set
- Check Docker credential chain is available inside the pod
- For cloud registries, verify workload identity is configured
- See [Container Registry](../configuration/registry.md) for auth details

### Build Runs Out of Memory

**Symptom:** Build Job pod is OOMKilled during Buildah execution.

The default memory limit is 512Mi, but Buildah with VFS storage driver can use 1-2Gi depending on the project.

**Fix:** Increase the memory limit in your Helm values:

```yaml
resources:
  limits:
    memory: 2Gi
  requests:
    memory: 512Mi
```

## Deployments Failing

### Knative Service Not Becoming Ready

```bash
# Check Knative Service status
kubectl get ksvc -A

# Check for detailed conditions
kubectl describe ksvc <artifact-name> -n <namespace>

# Check Knative controller logs
kubectl logs -n knative-serving deploy/controller

# Check Kourier logs
kubectl logs -n kourier-system deploy/3scale-kourier-control
```

**Common causes:**
- Image pull failure in the artifact pod (registry credentials not propagated)
- Insufficient resources in the cluster for the new pod
- Knative domain not configured correctly

### Artifact URL Not Reachable

**Knative deployments:**
- Verify DNS resolution: `nslookup <artifact-name>.default.vibed.example.com`
- Verify Kourier LoadBalancer has an external IP: `kubectl get svc -n kourier-system`
- Check that the wildcard DNS record points to the correct IP

**Kubernetes deployments:**
- Check NodePort allocation: `kubectl get svc -n <artifact-namespace>`
- Verify firewall rules allow traffic to the NodePort range (30000-32767)

### Instant Preview Not Used (deploy went through Buildah instead)

A deploy you expected to be a fast-path preview (`mode: preview`) instead built
a container image. The [dependency gate](../concepts/instant-preview.md#the-dependency-gate)
silently falls back to the build path when an app isn't eligible. Check:

- **Fast path enabled?** `config.fastPath.enabled: true` and a `runners` entry
  for the language.
- **Dependency not pre-baked?** Every dependency in `requirements.txt` /
  `package.json` must be in the runner image's manifest
  (`internal/prebaked/manifests/*.yaml`). One unknown dependency → full build.
- **Un-analyzable `requirements.txt`?** Lines like `-r other.txt`, VCS installs
  (`git+https://…`), or local paths make the file un-analyzable → full build.
- To force the fast path and get a clear error instead of a silent fallback,
  pass `target: "runner"` explicitly to `deploy_artifact`.

### Instant Preview Stuck or Failing

```bash
# The runner pool runs on Sandbox CRs — confirm the CRD is installed
kubectl get crd sandboxes.agents.x-k8s.io

# Inspect the warm pool (idle + claimed runner pods)
kubectl get sandboxes -n <fastPath.namespace> -l app.kubernetes.io/component=runner-pool

# vibeD logs show pool churn, claims, and warmup failures
kubectl logs -n vibed-system deploy/vibed | grep -iE "runner|pool"
```

**Common causes:**
- **agent-sandbox CRD missing** — without it the pool can't create runners; the
  RunnerDeployer isn't registered and deploys fall back to the build path.
- **Runner image not pullable** — pooled pods stay un-Ready; vibeD discards them
  after `fastPath.readyTimeout` and `vibed_pool_runners_created_total{status="failed"}`
  climbs. Verify the `runners.<lang>.image` ref and registry credentials.
- **Pool exhausted** — every claim is `cold` in `vibed_pool_claims_total`; raise
  `poolSize` or check why runners aren't replenishing.
- **Preview URL not reachable from outside the cluster** — runner URLs are
  in-cluster (`*.svc.cluster.local`). This is expected; promote the artifact for
  a durable, externally-routed deployment, or port-forward to the runner's
  Service to view the preview.

## Authentication Issues

### 401 Unauthorized

**Check the API key matches:**

```bash
# View the stored key
kubectl get secret vibed-auth -n vibed-system \
  -o jsonpath='{.data.api-key}' | base64 -d

# Verify the key works
curl -H "Authorization: Bearer <your-key>" \
  https://vibed.example.com/healthz
```

**Common causes:**
- Key mismatch between client and secret
- Missing `Authorization: Bearer` header prefix
- TLS issues (connecting via HTTP when TLS is enabled)
- Secret name doesn't match `auth.existingSecret` in values

For full auth configuration, see [Authentication & HTTPS](../configuration/authentication.md).

## Storage Issues

### PVC Stuck in Pending

```bash
kubectl get pvc -n vibed-system
kubectl describe pvc vibed-data -n vibed-system
```

**Common causes:**

| Cause | Fix |
|-------|-----|
| No default StorageClass | Set `persistence.storageClass` explicitly |
| StorageClass doesn't exist | Create it or use an existing one: `kubectl get sc` |
| No available PVs (static provisioning) | Create a PV matching the PVC spec |
| Quota exceeded | Check resource quotas: `kubectl get quota -n vibed-system` |

### Artifacts Lost After Restart

If artifact metadata disappears after a pod restart, you are likely using the in-memory store.

**Fix:** Switch to ConfigMap-backed store:

```yaml
config:
  store:
    backend: "configmap"
    configmap:
      name: "vibed-artifacts"
```

:::warning
The `memory` store backend is for development only. Always use `configmap` in production.
:::

### GitHub/GitLab Storage Failures

**Symptom:** Deploy succeeds locally but fails to push to Git storage.

**Check:**
- Token has required scopes (`repo` for GitHub, `api` for GitLab)
- Token hasn't expired
- Repository exists and is accessible
- Token is correctly resolved: check `env:VAR_NAME` variable is set in the pod

See [Storage Backends](../configuration/storage.md) for configuration details.

## Helm Upgrade Issues

### ConfigMap Changes Not Applied

The Deployment template includes a `checksum/config` annotation that triggers a rolling update when the ConfigMap content changes. If your config changes are not being applied:

```bash
# Check if rollout happened
kubectl rollout status deployment/vibed -n vibed-system

# Force a rollout if needed
kubectl rollout restart deployment/vibed -n vibed-system

# Verify the ConfigMap content
kubectl get configmap vibed-config -n vibed-system -o yaml
```

**Common cause:** Using `helm upgrade` without the correct values file. Always pass all values:

```bash
helm upgrade vibed deploy/helm/vibed/ \
  --namespace vibed-system \
  -f values-production.yaml
```

## Diagnostic Commands

Quick reference for common debugging commands:

| Command | Purpose |
|---------|---------|
| `kubectl get pods -n vibed-system` | Check pod status |
| `kubectl logs -n vibed-system deploy/vibed` | View vibeD logs |
| `kubectl logs -n vibed-system deploy/vibed --previous` | View logs from crashed pod |
| `kubectl describe pod -n vibed-system -l app.kubernetes.io/name=vibed` | Detailed pod info |
| `kubectl get events -n vibed-system --sort-by=.lastTimestamp` | Recent events |
| `kubectl get ksvc -A` | List Knative Services |
| `kubectl get pvc -n vibed-system` | Check persistent volumes |
| `kubectl get secret -n vibed-system` | List secrets |
| `curl -s http://localhost:8080/healthz \| jq` | Liveness check (via port-forward) |
| `curl -s http://localhost:8080/readyz \| jq` | Readiness check (via port-forward) |
| `curl -s http://localhost:8080/metrics \| grep vibed_` | View Prometheus metrics |
