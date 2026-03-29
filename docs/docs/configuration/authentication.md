---
sidebar_position: 4
---

# Authentication & HTTPS

vibeD protects its MCP and API endpoints with bearer token authentication, using the official MCP SDK auth middleware. This ensures MCP clients like Claude Desktop, Cursor, and others can securely connect to your vibeD instance.

## Why Authenticate?

MCP clients do not trust unsecured tool servers. Without authentication:

- Any process on the network could invoke deployment tools
- Built artifacts and source code could be exposed
- There is no audit trail of who deployed what

## Quick Start

The fastest way to enable auth is via a single environment variable:

```bash
export VIBED_AUTH_API_KEY="vibed_sk_your_secret_key_here"
./vibed --config vibed.yaml --transport http
```

This automatically enables API key authentication. MCP clients then connect with:

```
Authorization: Bearer vibed_sk_your_secret_key_here
```

## Authentication Modes

### API Key Mode (Default)

Simple bearer tokens validated against a configured list. Best for single-user setups, CI/CD pipelines, and development.

```yaml
auth:
  enabled: true
  mode: "apikey"
  apiKeys:
    - key: "env:VIBED_API_KEY"      # Resolved from environment variable
      name: "default"
    - key: "vibed_sk_ci_deploy_key"  # Literal value
      name: "ci-pipeline"
      scopes: ["deploy"]
```

**Key features:**

- Keys can be literal values or resolved from environment variables using the `env:` prefix
- Keys also support `file:/path/to/token` to read tokens from files
- Each key has a human-readable `name` used as the user identity (UserID) for ownership
- Optional `scopes` restrict what the key can do (empty = unrestricted)
- Optional `storage` block overrides the storage backend for this user (see [Per-User Multi-Repo Storage](./storage.md#per-user-multi-repo-storage))
- Constant-time comparison prevents timing attacks

### OIDC Mode

For production environments with an OpenID Connect identity provider (Keycloak, Auth0, Okta, Google). vibeD validates JWT tokens directly against the provider's JWKS endpoint.

```yaml
auth:
  enabled: true
  mode: "oidc"
  oidc:
    issuer: "https://auth.example.com/realms/vibed"   # OIDC issuer URL
    audience: "vibed"                                    # Expected audience claim
    usernameClaim: "preferred_username"                   # JWT claim for username
    emailClaim: "email"                                  # JWT claim for email
    roleClaim: "realm_access.roles"                      # JWT claim for roles
    adminRole: "vibed-admin"                             # Role value that grants admin access
    scopes:                                              # Scopes to request
      - "openid"
      - "profile"
      - "email"
```

vibeD publishes an [OAuth Protected Resource Metadata](https://datatracker.ietf.org/doc/html/rfc9728) endpoint at `/.well-known/oauth-protected-resource` so MCP clients can discover the authorization server automatically.

**Environment variable overrides:**

| Variable | Description |
|----------|-------------|
| `VIBED_AUTH_OIDC_ISSUER` | OIDC issuer URL |
| `VIBED_AUTH_OIDC_AUDIENCE` | Expected audience claim |
| `VIBED_AUTH_OIDC_ADMIN_ROLE` | Role that grants admin access |

### OAuth Proxy Mode

For environments where an external OAuth gateway or reverse proxy validates tokens. vibeD trusts the proxy to authenticate requests.

```yaml
auth:
  enabled: true
  mode: "oauth"
```

The external proxy (e.g., OAuth2 Proxy, Pomerium, or an API gateway) should:

1. Validate the OAuth token against your identity provider
2. Forward the request to vibeD with the original `Authorization: Bearer` header
3. Set the `X-Forwarded-User` header with the authenticated user's identity

## What Gets Protected

| Endpoint | Authentication |
|----------|----------------|
| `/mcp/*` | **Required** when auth is enabled |
| `/api/*` | **Required** when auth is enabled |
| `/api/share/*` | Always open (public share links) |
| `/healthz` | Always open (Kubernetes liveness probe) |
| `/readyz` | Always open (Kubernetes readiness probe) |
| `/metrics` | Always open (Prometheus scraping) |
| `/` (dashboard) | Always open (static frontend assets) |

## HTTPS / TLS

Since bearer tokens are sent on every request, running without TLS exposes credentials on the network. vibeD supports three TLS configurations:

### Certificate Files (Production)

Use certificates from cert-manager, Let's Encrypt, or your PKI:

```yaml
auth:
  tls:
    enabled: true
    certFile: "/etc/vibed/tls/tls.crt"
    keyFile: "/etc/vibed/tls/tls.key"
```

Or via environment variables:

```bash
export VIBED_TLS_ENABLED=true
export VIBED_TLS_CERT_FILE=/etc/vibed/tls/tls.crt
export VIBED_TLS_KEY_FILE=/etc/vibed/tls/tls.key
```

### Auto-Generated Self-Signed Certificate (Development)

For local development and testing, vibeD can generate a self-signed certificate automatically:

```yaml
auth:
  tls:
    enabled: true
    autoTLS: true
```

Or:

```bash
export VIBED_TLS_ENABLED=true
export VIBED_TLS_AUTO=true
```

The self-signed certificate covers `localhost`, `127.0.0.1`, `::1`, and the system hostname. MCP clients will need to trust this certificate or skip verification.

:::warning
Do **not** use `autoTLS` in production. Use proper certificates from a trusted CA.
:::

### TLS Termination at Ingress (Kubernetes)

In Kubernetes, TLS is typically terminated at the Ingress controller. In this case, disable TLS in vibeD and configure your Ingress:

```yaml
# vibed.yaml — no TLS needed, Ingress handles it
auth:
  enabled: true
  mode: "apikey"
  # tls not enabled — Ingress terminates TLS
```

```yaml
# Kubernetes Ingress with cert-manager
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: vibed
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - vibed.example.com
      secretName: vibed-tls
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

## Helm Chart Setup

### Enable Auth with a Secret

Create a Kubernetes Secret with your API key:

```bash
kubectl create secret generic vibed-auth \
  --from-literal=api-key="vibed_sk_your_secret_key_here"
```

Then enable auth in your Helm values:

```yaml
auth:
  enabled: true
  mode: "apikey"
  existingSecret: "vibed-auth"  # References the Secret above
```

### Enable TLS with cert-manager

Create a Certificate resource:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: vibed-tls
spec:
  secretName: vibed-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
    - vibed.example.com
```

Then enable TLS in your Helm values:

```yaml
auth:
  enabled: true
  mode: "apikey"
  tls:
    enabled: true
    existingSecret: "vibed-tls"  # References the cert-manager Secret
```

## Artifact Ownership

When authentication is enabled, vibeD enforces per-user artifact isolation:

- **Deploy** stamps each artifact with the deploying user's `owner_id` (the API key's `name` field)
- **List** only returns artifacts owned by the current user
- **Status**, **Update**, **Delete**, and **Logs** verify ownership before proceeding
- Accessing another user's artifact returns "not found" (not "forbidden") to avoid leaking artifact existence

When authentication is disabled, ownership checks are skipped and all users see all artifacts.

### Per-User Storage

Each API key can optionally define a dedicated storage backend (GitHub or GitLab repository), ensuring complete artifact isolation. See [Per-User Multi-Repo Storage](./storage.md#per-user-multi-repo-storage) for configuration details.

## Connecting MCP Clients

### Claude Desktop

Add vibeD as a remote MCP server in Claude Desktop settings:

```json
{
  "mcpServers": {
    "vibed": {
      "url": "https://vibed.example.com/mcp/",
      "headers": {
        "Authorization": "Bearer vibed_sk_your_secret_key_here"
      }
    }
  }
}
```

### Environment Variables Reference

| Variable | Description | Example |
|----------|-------------|---------|
| `VIBED_AUTH_API_KEY` | Set a single API key (auto-enables auth) | `vibed_sk_...` |
| `VIBED_AUTH_ENABLED` | Enable/disable authentication | `true` |
| `VIBED_AUTH_MODE` | Authentication mode | `apikey`, `oidc`, or `oauth` |
| `VIBED_AUTH_OIDC_ISSUER` | OIDC issuer URL | `https://auth.example.com` |
| `VIBED_AUTH_OIDC_AUDIENCE` | OIDC expected audience | `vibed` |
| `VIBED_AUTH_OIDC_ADMIN_ROLE` | OIDC role granting admin | `vibed-admin` |
| `VIBED_TLS_ENABLED` | Enable HTTPS | `true` |
| `VIBED_TLS_CERT_FILE` | Path to TLS certificate | `/etc/tls/tls.crt` |
| `VIBED_TLS_KEY_FILE` | Path to TLS private key | `/etc/tls/tls.key` |
| `VIBED_TLS_AUTO` | Generate self-signed cert | `true` |
