---
slug: citizen-developers
title: "A governed home for your citizen developers' AI apps"
authors: [max]
tags: [governance, use-case]
---

Something is happening in every company right now: people who aren't professional developers are using AI to **vibe-code small tools**. A dashboard for their team. A form that writes to a spreadsheet. A scraper that watches a supplier's price page. The tool works — and then it lives on a laptop, or gets pasted into some third-party host with a company API key baked into it.

That's the classic **shadow-IT** problem, except now it happens at AI speed and in far greater volume. The instinct is to lock it down. But telling a motivated employee "no" just pushes the tool somewhere you can't see. The better answer is to give that app **a place to land** — self-hosted, isolated, governed — that's so fast and easy it beats the laptop.

That's the problem vibeD exists to solve.

<!-- truncate -->

## The citizen developer

vibeD's target user has always been the **employee who vibe-coded a small tool with GenAI and wants it captured into a governed sandbox instead of living on their laptop**. They're not going to file a ticket, wait for a platform team, or learn Kubernetes. They point their AI agent at vibeD (over MCP or the HTTP API), and seconds later their app is at a real URL.

The company's job is different: make sure that when a hundred of those apps show up, each one is **isolated, scoped, owned, and auditable** — without adding the friction that sends people back to the laptop. Everything below ships in the open-source core.

## 1. Untrusted code, contained

Citizen-developer apps are, by definition, code nobody reviewed. So vibeD runs each one in a **Kata + Firecracker microVM** — hardware-grade isolation, not a shared container on a shared host. A tool that misbehaves (or that the model got subtly wrong) is boxed into its own microVM with its own kernel. The blast radius is one app, not your cluster or someone's workstation.

See [lanes and templates](/docs/concepts/lanes-and-templates).

## 2. The app can only reach what it declares

The scariest thing about an AI-written tool isn't that it crashes — it's what it can *reach*. vibeD makes every app declare its **outbound allow-list** at deploy time, and enforces it at the network with a default-deny egress proxy:

```json
{"name": "supplier-watcher", "allowed_hosts": ["api.supplier.com"]}
```

That app can talk to `api.supplier.com` and nothing else — not your internal network, not the instance metadata endpoint, not an exfiltration server. Wildcards are anchored to real subdomains, and the only route out of a sandbox is through the authorizing proxy.

See [egress control](/docs/configuration/egress-control).

## 3. Only your people, with a real identity

vibeD plugs into your existing identity provider over **OIDC** (Keycloak, Okta, Entra, Google, …), or uses API keys for programmatic access. Authentication is on before vibeD is ever exposed, so only your employees can deploy or reach apps — and every deploy carries an **owner**. Deactivate someone in your IdP and their access goes with them.

See [authentication](/docs/configuration/authentication).

## 4. Ownership, teams, and quotas

Every app has an owner, and owners roll up into **departments**, each with its own namespace. Users see and manage their own apps; admins get the fleet view. **Per-owner and per-department quotas** cap how many concurrent apps a person or a team can run, hard-gated at the deploy path — so one enthusiastic team can't quietly consume the whole cluster.

See [quotas](/docs/configuration/quotas).

## 5. A record of what happened

Governance means being able to answer "who deployed that, and when?" vibeD keeps an **append-only audit trail** of the mutating actions — deploy, update, rollback, delete — with the actor, the target, and the outcome. It's the baseline you need when a security or compliance team asks how an app got there.

See [the audit log](/docs/configuration/audit-log).

## 6. Your images, your cluster, your supply chain

The built-in runtimes are a starting point, not a mandate. A platform or security team can **bring their own hardened base images** for any runtime slot, and vibeD's validator rejects images that don't match — including a strict mode that catches a mutable tag being re-pushed underneath you. Everything runs **on your own Kubernetes**, in your environment, so the data never leaves. Every vibeD image ships with an **SBOM and an in-registry attestation**.

See [custom base images](/docs/configuration/custom-base-images).

## Governance without the friction

The point of all of this is that it's **not a tradeoff**. A citizen developer still gets their app to a live URL in seconds — the isolation, the egress allow-list, the ownership, the audit entry all happen automatically, around the fast path, not in front of it. That's the only kind of governance that actually sticks: the kind people don't route around, because the governed path is also the *easy* path.

If your company is already seeing AI-built tools show up in places you can't see, that's the signal. Give them somewhere better to land.

Start with the [installation guide](/docs/getting-started/installation) and the [architecture overview](/docs/concepts/architecture). The code is on [GitHub](https://github.com/vibed-project/vibeD).
