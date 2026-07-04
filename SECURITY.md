# Security Policy

vibeD runs untrusted, AI-generated code inside your own Kubernetes cluster. Isolation and network containment are core design goals, not add-ons. This document covers how to report a vulnerability and the security model the project is built around.

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Report suspected vulnerabilities privately to the maintainers via [GitHub's private security advisory](https://github.com/vibed-project/vibeD/security/advisories/new) ("Report a vulnerability"). If you cannot use that channel, email the maintainers at **security@vibed-project.dev**.

Please include, where you can:

- A description of the issue and the impact you believe it has
- The affected component(s) — e.g. `vibed`, `vibed-controller`, `vibed-router`, `vibed-agent`, `vibed-egress-authz`, `vibed-workerd-loader`
- Steps to reproduce, or a proof of concept
- The version / commit you tested against and relevant configuration (auth mode, network posture)

We aim to acknowledge a report within a few business days and will keep you updated as we investigate. Please give us a reasonable window to release a fix before any public disclosure. We are happy to credit reporters in the advisory unless you prefer to remain anonymous.

## Supported Versions

vibeD is experimental and pre-1.0. Security fixes land on `main` and in the latest tagged release. We generally do not backport fixes to older minor versions — please track the most recent release.

| Version | Supported |
|---------|-----------|
| Latest release (`main`) | ✅ |
| Older releases | ❌ (upgrade to the latest) |

## Security Model

vibeD's threat model assumes that **the code being deployed is untrusted**. The deploy path never builds a container image; instead, source is injected into a pre-booted sandbox and run there. The defenses below constrain what that code can do once it is running.

### Workload isolation

A deterministic classifier routes each deploy to one of two lanes based on its files:

- **Fast lane** — static sites (served by an nginx sandbox) and small workers (workerd V8 isolates). Sub-second start.
- **General lane** — arbitrary Node.js, Python, Go, or any base image runs inside a **Kata + Firecracker microVM**, giving each app hardware-grade (VM-level) isolation from the host kernel and from other tenants. This is the isolation boundary for arbitrary untrusted code.

The control plane itself is stateless; all persistent state lives in a pluggable store, so the components that face untrusted workloads hold no long-lived secrets of their own.

### Default-deny egress

Outbound network access from a sandbox is **default-deny**. When per-app egress control is enabled, all sandbox egress is forced through a Squid forward proxy, and every outbound connection is authorized per-connection by `vibed-egress-authz`:

- Squid consults the authorizer via an `external_acl` helper for each request (the `squid-helper` loop in `internal/egressauthz`). The helper **fails closed**: any error, timeout, or unreachable authorizer denies the connection.
- The authorizer maps the source sandbox pod IP to the owning `VibedApp` and checks the destination host against that app's allow-list (`spec.egress.allowedHosts`). vibeD's own system hosts (e.g. the source store) are always permitted; nothing else is.
- Allow-list matching is case-insensitive and port-insensitive. A bare `example.com` matches only that exact host; a `*.example.com` wildcard matches subdomains but not the apex. An **empty allow-list means the app can reach no external hosts at all.**
- Every denied connection is logged for the audit trail.

Because the allow-list is a property of the `VibedApp` spec, egress policy is set per app and evaluated at connection time — updating the spec updates the policy without redeploying.

### Network policy

In the production posture, sandboxes run under a vibeD-owned Kubernetes `NetworkPolicy` (Helm `networkPolicy.enabled`, paired with `runtime.sandboxNetworkPolicy: Unmanaged`). The policy is default-deny, then admits only:

- Control-plane → sandbox traffic on the ports vibeD needs
- DNS to a specific, selectable set of cluster-DNS pods only — so untrusted code cannot exfiltrate data by tunnelling it to an arbitrary external DNS server
- The egress path required for the agent to pull source, while blocking arbitrary cluster-internal egress

The DNS selector defaults to upstream CoreDNS in `kube-system` and is configurable for clusters that label their DNS differently. In dev/Kind, sandboxes run unmanaged and fully open for convenience — the locked-down posture is a production configuration.

### Authentication and authorization

HTTP endpoints (`/mcp`, `/api/`, `/v1/`, and the in-cluster source path) are protected by bearer-token auth built on the MCP SDK's `RequireBearerToken` middleware, which binds sessions to users. Three modes ship in the core (`internal/auth`):

- **`apikey`** — bearer tokens compared in constant time against configured keys, plus runtime-generated per-user keys matched by SHA-256 hash. Users are auto-provisioned on first use.
- **`oauth`** — passthrough for an external OAuth gateway/proxy: the bearer token must match a configured proxy shared secret, after which the trusted `X-Forwarded-User` header identifies the user.
- **`oidc`** — direct JWT validation against an OIDC issuer (e.g. Keycloak, Azure Entra). The audience (client ID) is **required** so tokens minted for another app cannot be replayed against vibeD. Roles are read from a configurable claim.

Additional behavior worth noting:

- **Suspended-user enforcement.** On every authenticated request, a user whose store status is `suspended` is rejected with `401`, even if their token still verifies. Revoking access does not wait for token expiry.
- **Role-based access.** An admin role (from config or an OIDC role claim) gates administrative endpoints. When auth is disabled (dev only), requests run as a local admin — so **enable auth before exposing vibeD on any untrusted network.**
- Health, metrics, API docs, well-known, and public share endpoints are intentionally unauthenticated; secrets are never served on them.

Secrets in configuration (API keys, proxy secrets) support `env:` and `file:` indirection so they need not be written literally into config.

### Audit trail

Mutating actions — deploy, delete, rollback — are recorded to an **append-only** audit store (`internal/audit`, backed by the pluggable store). Each event captures the actor (resolved from the request context), action, target, outcome (`ok` / `denied` / `error`), a timestamp, and optional enrichment such as tenant, session, source hash, and policy decision. Events are also mirrored to structured logs and counted in a Prometheus metric.

The recorder can be configured **fail-closed**: if it cannot persist an event, the mutating action is rejected rather than allowed to proceed untraced, preferring an availability loss over an unrecorded change.

### Governance controls are configurable

The egress allow-lists, network policy, authentication mode, suspended-user enforcement, audit trail (including fail-closed persistence), and role gating described above are all configuration, enabled and tuned through the app config and Helm values. The defaults favor a safe production posture; dev overlays deliberately relax them for local iteration. vibeD's provider/registry seams (for example, the auth-mode registry) let operators and integrators add their own modes and backends from an out-of-tree Go module without forking the core; the in-tree defaults enable the full feature set.

## Scope and Assumptions

- vibeD assumes a correctly configured cluster: a Kata `RuntimeClass`, a dedicated sandbox node pool, and the production NetworkPolicy/egress settings enabled. Running the general lane **without** Kata isolation, or with sandboxes fully open, is a dev-only configuration and is not a supported production security posture.
- vibeD does not attempt to sandbox the code you write and run in the control plane's own configuration; treat config, secrets, and Helm values as trusted inputs.
- Reports about missing hardening in the explicitly dev-only paths (e.g. no-auth mode, unmanaged sandbox networking in Kind) are useful, but note that these are intended to be replaced by the production posture before exposure.

Thank you for helping keep vibeD and its users safe.
