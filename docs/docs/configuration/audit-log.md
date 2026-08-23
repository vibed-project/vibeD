---
sidebar_position: 8
---

# Audit Trail

vibeD records mutating actions to an append-only audit trail so you can answer "who changed what, when", a baseline enterprise-governance requirement. The trail is always on; what differs is whether it persists (see [Storage](#storage)) and whether a failed write blocks the action (see [Fail-closed mode](#fail-closed-mode)).

## What's recorded

Every recorded event is an `AuditEvent`. The first block of fields is **always present**:

| Field     | Meaning                                                        |
| --------- | ------------------------------------------------------------- |
| `time`    | UTC timestamp of the action.                                  |
| `actor`   | Authenticated user ID (`""` when unauthenticated).            |
| `action`  | `deploy`, `delete`, or `rollback`.                            |
| `target`  | App/artifact name the action operated on.                     |
| `outcome` | `ok`, `denied`, or `error`.                                   |
| `detail`  | Error message or context, when relevant (omitted otherwise).  |

Actions and their outcomes:

| Action     | Recorded when                                  | Outcomes                |
| ---------- | ---------------------------------------------- | ----------------------- |
| `deploy`   | a new app or redeploy (via API or MCP)         | `ok`, `denied`, `error` |
| `delete`   | an app is deleted (via API or MCP)             | `ok`, `error`           |
| `rollback` | an artifact is rolled back to a prior version  | `ok`, `error`           |

`outcome=denied` is how a [quota](./quotas.md) rejection shows up; `error` carries the failure reason in `detail`.

## Enrichment fields

Beyond the always-present block, each event carries optional **enrichment fields**. They are omitted from JSON when empty:

| Field             | Meaning                                       | Populated by                                     |
| ----------------- | --------------------------------------------- | ------------------------------------------------ |
| `tenant_id`       | Tenant the action belonged to.                | Core, on `deploy`, `delete`, and `rollback`.     |
| `source_hash`     | SHA-256 of the exact deployed source tarball. | Core, on `deploy` (source provenance).           |
| `session_id`      | Auth session identifier.                       | An out-of-tree recorder/store.                   |
| `policy_decision` | Deploy-time policy verdict.                    | An out-of-tree recorder/store.                   |
| `before`          | Prior state, for a governance diff.            | An out-of-tree recorder/store.                   |
| `after`           | New state, for a governance diff.              | An out-of-tree recorder/store.                   |

The core populates `tenant_id` on every mutating action and `source_hash` on deploys, a hash of the exact tarball that was injected into the sandbox, so an event ties an action to the precise bytes that ran. The remaining fields (`session_id`, `policy_decision`, `before`, `after`) are left empty by the core; they exist so an out-of-tree recorder or audit store (for example one that adds tamper-evidence or SIEM export) can enrich events without changing the recorder interface.

Enrichment is carried on the request context: a caller attaches `audit.Fields` to the context, and every event recorded under that context merges the non-empty values. Empty fields are ignored, so partial enrichment is fine.

## Querying it

Admins read the trail over the API (the `admin` role is required; non-admins get `403`):

```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  "https://vibed.example.com/v1/audit?actor=alice&action=deploy&app=my-site&limit=100"
```

```jsonc
{ "events": [
  {
    "time": "2026-05-24T10:02:11Z",
    "actor": "alice",
    "action": "deploy",
    "target": "my-site",
    "outcome": "ok",
    "tenant_id": "acme",
    "source_hash": "9f2c…e1"
  },
  { "time": "2026-05-24T09:58:03Z", "actor": "bob", "action": "delete", "target": "old-demo", "outcome": "ok", "tenant_id": "acme" }
] }
```

All filters (`actor`, `action`, `app`, `limit`) are optional; results come back newest-first. Filtering is on the always-present fields only; enrichment fields are returned but not queryable.

## Storage

The audit trail is written to a **pluggable audit store**, the same store backend that holds the rest of vibeD's state:

- **SQLite backend** (`store.backend: sqlite`): events persist in the same database as everything else and survive restarts. Enrichment fields are persisted alongside the core fields.
- **Memory / ConfigMap backends**: the trail is kept **in memory only** and is lost on restart (vibeD logs a warning at startup). The ConfigMap backend does not implement the audit store, so the server falls back to an in-memory audit log there. Use SQLite for a durable audit trail.

Because the store is an interface, an operator or integrator building their own out-of-tree Go module can supply an alternative audit-store implementation (for example a tamper-evident or externally-exported store) without changing the recorder.

Every recorded event also increments `vibed_audit_events_total{action,outcome}` and is mirrored to structured logs, regardless of the store backend.

## Fail-closed mode

By default the audit write is **fail-open**: if the persistent store rejects an append, vibeD logs a warning and lets the action proceed. This suits dev clusters and the default install, which may not have a persistent audit store wired.

Set `audit.failClosed: true` to make the trail **fail-closed**: a mutating action whose audit event cannot be persisted is rejected, so an untraceable mutation never happens. This prefers availability loss over a compliance gap.

```yaml
audit:
  failClosed: false   # true → reject the action when the audit write fails
```

Fail-closed only affects the persistent store write. The Prometheus counter and structured log are updated on every event whether or not the store append succeeds, and a `nil` (unconfigured) recorder is a no-op that never blocks. Production `values.yaml` sets `failClosed: true`.

:::note Egress denials
Blocked outbound connections are logged separately by the egress proxy's authorizer (see [Egress Control](./egress-control.md)), not in this trail, because they happen in a different process on the request hot path.
:::
