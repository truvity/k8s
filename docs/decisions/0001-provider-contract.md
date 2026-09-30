# 0001 — One provider-neutral cluster contract

**Status:** Accepted

## Context

Clusters are provisioned by more than one kind of component: EKS Auto Mode
first, self-hosted Talos second. What consumes a cluster — chart installers,
the access layer, backup jobs, the things that wire workload identity — does
not care which. If each consumer learns each provider, every new provider is
a change in every consumer.

## Decision

`pkg/cluster` defines the contract, and every provider component reports it.

Four facts, always present: **name**, **endpoint**, **certificate authority**
and **OIDC issuer** (the issuer only with workload identity). And a set of
capabilities, each a thing a consumer may rely on when present:

| Capability | Meaning |
| --- | --- |
| node pools | capacity in declared groups, scaling on demand |
| storage | a default StorageClass with dynamic provisioning |
| network policy | enforced NetworkPolicy |
| workload identity | ServiceAccount tokens exchangeable for outside credentials |
| load balancing | externally reachable LoadBalancer or Gateway |
| API access | API reachable from the places the caller named |

`Outputs.Validate` is the single check. A consumer validates at the boundary
and asks for capabilities; `Provider` is informational and a consumer that
branches on it is outside the contract.

EKS Auto Mode is the first implementation, because it is the estate's
running shape and the extraction has a proof (see 0002). Self-hosted Talos
is the second, and its purpose here is to keep the contract honest: a
capability that only EKS can ever give is not a contract capability.

## Consequences

- A new provider is a new package that returns `cluster.Outputs`; nothing in
  a consumer changes.
- Adding a capability is a decision of its own, with a page here, because it
  changes what every provider must say yes or no to.
- A provider leaves out a capability it cannot honour; it never claims one
  it cannot.
- Provider-specific facts (an account, a region, a node role) are the
  provider component's outputs, not the contract's.
