# 0004 — The tenancy chart is deprecated, with a window

**Status:** Accepted. Nothing is removed by this decision.

## Context

`charts/tenancy` renders a tenant's whole kit: its Namespace, a baseline
NetworkPolicy, a quota, RBAC, a NATS account and Pod Identity ServiceAccounts.
That is one chart deciding policy for many concerns. The model it is moving to
splits them: what a tenant namespace must be is a contract (guidelines a
tenant's own chart is held to), the thin per-tenant values are data, and what a
tenant may do in AWS and NATS is its own identity ServiceAccount. The objects
that remain generic (Namespace and Pod Security labels, the baseline
NetworkPolicy, quotas) already have a home in the charts that render them for
any namespace: `cluster-foundation`, `cluster-baseline` and
`cluster-network-policies`.

## Decision

1. **`charts/tenancy` is deprecated.** `Chart.yaml` says `deprecated: true` and
   the description points here; Helm prints the notice on install and pull. The
   chart keeps rendering exactly what it rendered.
2. **Its replacement is guidelines, thin values and an identity.** The tenancy
   guidelines are a contract in `truvity/policy`; a tenant is a few values; each
   tenant gets an identity ServiceAccount through the platform's identity
   mechanism. Nothing is added to this chart to bridge to them.
3. **The window.** From the first release that carries this notice until all of
   the following hold:
   - the contract is published in `truvity/policy`;
   - no cluster of the estate installs the chart (each consumer has moved to the
     replacement);
   - two further releases, and at least 60 days, have passed since the last of
     the above.

   During the window the chart receives correctness and security fixes only: no
   new values, no new features, and no change to the objects it renders for the
   values it already accepts.
4. **Removal is its own change.** It is a release of its own with a `Breaking:`
   entry that says what to use instead, and it is made only after the window.
   Until then the chart stays in the release's chart list, so the published
   versions stay reproducible.

## Consequences

- Consumers see a deprecation notice and a date-free, condition-based window;
  they are not forced to move before the replacement exists.
- A change that wants a new tenancy value is refused here and goes to the
  replacement.
- The window has a defined end, so the chart cannot linger: the removal change
  is a tracked item, not an intention.
