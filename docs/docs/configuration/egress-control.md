---
sidebar_position: 6
---

# Egress Control

By default a sandbox can reach the public internet (the NetworkPolicy only blocks cluster-internal addresses). For enterprise data-governance you usually want the opposite: a sandbox should reach **only** the hosts its app explicitly needs. vibeD's egress control enforces a **per-app hostname allow-list** that untrusted code cannot bypass.

## How it works

```
sandbox --HTTPS_PROXY--> [Squid egress proxy] --asks--> [vibed-egress-authz]
                              │  allow → dst host          (src podIP → VibedApp → allowedHosts?)
                              │  deny  → 403 (logged)
NetworkPolicy: a sandbox may egress ONLY to the proxy — there is no direct internet path.
```

- The sandbox NetworkPolicy permits egress **only to the Squid proxy** (port 3128) plus DNS. The broad `0.0.0.0/0` rule is removed, so raw sockets and non-HTTP traffic simply can't leave.
- Sandboxes get `HTTP(S)_PROXY` pointed at Squid, so their HTTP libraries route through it.
- For every connection, Squid asks **`vibed-egress-authz`**, which maps the source pod IP → the owning `VibedApp` → its `egress.allowedHosts`, and allows or denies (default-deny). vibeD's **system hosts** (e.g. the S3/MinIO source store) are always allowed so the agent can still pull source. Denials are logged.

:::note Metadata / DNS-rebinding protection
The allow-list authorizes a **hostname**, but the decision to connect is also enforced on the **resolved IP** — so an allow-listed domain whose DNS answers `169.254.169.254` (the cloud metadata endpoint) or a link-local/loopback address is still refused. `vibed-egress-authz` rejects a host that resolves to a metadata/link-local range, and Squid additionally denies these ranges by the IP it actually connects to. In the production posture (`storage.tarball.backend: s3`, external object store) Squid also denies RFC1918 / IPv6-ULA ranges — there's no legitimate in-cluster egress target. With the in-cluster `served` source store those private ranges stay reachable so source injection keeps working.
:::

## Declaring an app's allowed hosts

Per app, via the deploy metadata or the MCP tool:

```jsonc
// POST /v1/deploy  metadata
{ "name": "my-app", "egress": { "allowed_hosts": ["api.openai.com", "*.internal.example.com"] } }
```

```jsonc
// deploy_artifact (MCP)
{ "name": "my-app", "files": { ... }, "allowed_hosts": ["api.openai.com"] }
```

This lands on `VibedApp.spec.egress.allowedHosts`. Matching is case- and port-insensitive; `*.example.com` matches any subdomain (`a.example.com`, `a.b.example.com`) but **not** the apex `example.com`. **Omitting the list = no external egress** (default-deny).

## Enabling it

Egress control is off by default. Turn it on alongside vibeD's own sandbox NetworkPolicy:

```yaml
runtime:
  sandboxNetworkPolicy: Unmanaged   # vibeD owns the policy
networkPolicy:
  enabled: true                     # the locked-down sandbox policy
egressControl:
  enabled: true
  systemHosts:                      # always reachable (don't lock yourself out of the source store)
    - s3.us-east-1.amazonaws.com
```

This deploys the Squid proxy + `vibed-egress-authz`, rewrites the sandbox NetworkPolicy to proxy-only egress, and injects the proxy env into sandboxes.

:::tip Source store
With egress control on, the agent pulls source **through the proxy** too — so your `storage.tarball.s3` endpoint host must be in `egressControl.systemHosts`, or source injection will be denied.
:::

:::caution Non-HTTP egress
The proxy speaks HTTP/HTTPS (CONNECT) only, and the NetworkPolicy blocks everything else — so a sandbox can't open arbitrary raw TCP/UDP connections at all. That's intentional for web apps; workloads needing other protocols aren't supported under egress control.
:::

## Observing denials

`vibed-egress-authz` logs each denied connection (`egress denied src=… host=…`), and Squid's access log records allow/deny per request. These feed the deploy/egress **audit trail** (a later governance milestone).
