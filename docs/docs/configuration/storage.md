---
sidebar_position: 2
---

# Source Storage

When an app is deployed, its source tree is a gzipped tarball that `vibed-agent` pulls into the sandbox at start time. vibeD stores that tarball in a pluggable **source-blob store**, selected by `storage.tarball.backend`.

## `served` (development only)

vibeD keeps the tarball on its own PVC and serves it at an in-cluster URL (`/internal/sources/...`). The agent pulls from the vibeD Service's cluster-DNS name.

```yaml
storage:
  tarball:
    backend: "served"
    served:
      basePath: "/data/vibed/sources"
      publicBaseURL: ""   # empty -> http://<vibed-svc>.<ns>.svc.cluster.local:8080
```

This works **only in dev**, where sandboxes run `Unmanaged` and keep normal cluster DNS. Once a restrictive NetworkPolicy is in place (production), sandboxes can't resolve cluster DNS or reach in-cluster Services, so `served` fails.

:::tip
Leave `publicBaseURL` empty so it defaults to the Service DNS name. Don't hardcode the Service ClusterIP — it changes on every reinstall and silently breaks the agent's source pull.
:::

## `s3` (production)

vibeD streams the tarball to S3 (or MinIO) and hands the agent a **pre-signed URL** the sandbox reaches over its allowed public egress. This is the only backend that works under a locked-down sandbox NetworkPolicy.

```yaml
storage:
  tarball:
    backend: "s3"
    s3:
      endpoint: ""        # empty for AWS S3; set for MinIO
      bucket: "vibed-sources"
      region: "us-east-1"
      presignTTL: "15m"
```

Credentials come from the standard AWS SDK chain (env vars, IRSA, instance profile).

## Why the dev/prod split exists

agent-sandbox's network model gives sandbox pods no cluster-internal egress and no cluster DNS once a NetworkPolicy is enforced — by design, this is where enterprise data-egress controls live. A pre-signed S3 URL is a *public* (or external-egress-allowed) URL the sandbox can reach without touching the cluster network, which is exactly why `s3` is required in production. See the [production guide](../deployment/production-guide.md).

## App ownership

App metadata is keyed by the authenticated `owner`. With auth enabled, `GET /v1/apps` returns only the caller's apps, and get/delete check ownership. With auth disabled (dev), all apps are visible under the `admin` identity.
