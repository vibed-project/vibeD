---
sidebar_position: 7
---

# Deploy Quotas

Quotas cap how many concurrent apps a single user or a whole department may run, so one team (or a runaway agent) can't consume the cluster. They're off by default; when on, a deploy that would cross a ceiling is **hard-gated**: the API returns `429 Too Many Requests` and nothing is created.

## How it works

- Counts are taken over **live `VibedApp`s** using the `vibed.dev/owner` and `vibed.dev/department` labels vibeD stamps at deploy time.
- Only **new** apps are gated. A redeploy under an existing name is never counted (it reuses the CR), so iterating on an app you already own never trips the quota.
- Both ceilings are checked: a deploy must pass the per-owner **and** the per-department cap.
- A limit of `0` means "unlimited" for that axis.

```
deploy (new app) ──> per-owner count >= maxAppsPerOwner?      ─ yes ─> 429 (scope=owner)
                 └─> per-department count >= dept ceiling?     ─ yes ─> 429 (scope=department)
                 └─> otherwise: create, stamping owner+department labels
```

## Department resolution

The per-department ceiling needs to map an owner to a department. vibeD resolves `owner → user → department` through the **user store**, so per-department quotas require:

- the **SQLite** store backend (`store.backend: sqlite`), which is the only backend with a user store, and
- users assigned to a department, via the `department` field on an API key, or the OIDC `departmentClaim`.

When no department can be resolved (no user store, or a user with no department), only the per-owner ceiling applies.

## Configuration

```yaml
quotas:
  enabled: true
  maxAppsPerOwner: 5         # per-user ceiling (0 = unlimited)
  maxAppsPerDepartment: 20   # per-department aggregate ceiling (0 = unlimited)
  perDepartment:             # override the department ceiling by name
    platform: 50
    contractors: 3
```

With the above, any single user is capped at 5 apps; the `platform` department may run 50 in aggregate, `contractors` 3, and every other department 20.

## Observing rejections

Each rejection increments `vibed_quota_rejections_total{scope="owner|department"}`, and the deploy gets a `429` with a message naming the ceiling it crossed. The rejection is also written to the [audit trail](./audit-log.md) as a `deploy` event with `outcome=denied`.

## Concurrency caveat: this is a soft cap

The enforcer counts live `VibedApp`s and **then** the deploy path creates the new one, a List-then-Create pattern. Under burst traffic from a single owner, two concurrent deploys can both observe `count < max` and both succeed, briefly putting the owner at `max + 1` (or higher, scaled to the number of in-flight requests). The new apps are real; subsequent deploys reject normally once any of them is counted.

In other words, the ceiling is **eventually consistent**, not transactional. For most governance use cases (an owner runs ~5 apps, the cap is 10) this overshoot is invisible. If you need a strictly transactional cap (refusing the (N+1)th deploy under *any* race), the right shape is a `ValidatingAdmissionPolicy` (CEL) or an admission webhook that counts and gates inside the apiserver's optimistic-concurrency loop. That's tracked as future work; if you need it sooner, the quota counter still gives you alerting (`vibed_quota_rejections_total`) and the audit trail records every breach.

Redeploys are unaffected: they reuse the existing CR and skip the count entirely.
