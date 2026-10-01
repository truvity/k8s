# Changelog

The first release is v0.1.0 (its heading carries no date until the tag is cut).
Entries are written for someone deciding whether to bump: what changed for
them, and for anything breaking, what to do.

## v0.1.0

- `pkg/aws/pullthroughcache`: the ECR pull-through cache as a component,
  `truvity:k8s/aws:PullThroughCache`. One rule per upstream registry, and for
  an upstream that needs credentials a Secrets Manager secret (under the
  `ecr-pullthroughcache/` name prefix ECR requires) and its version. The
  caller supplies the credentials as Pulumi string inputs and may supply a
  naming hook; `LegacyTopLevel` aliases every child from its URN as a loose
  resource under the stack, so existing resources are adopted in place. It
  reports the prefixes so the caller can grant the pull permissions.

## v0.1.0

- The provider-neutral cluster contract (`pkg/cluster`): `Outputs` (name,
  endpoint, certificate authority, OIDC issuer, capabilities), the six
  capabilities (node pools, storage, network policy, workload identity, load
  balancing, API access) and `Outputs.Validate`, which refuses an outputs
  value a consumer could not safely use and reports every problem at once.
- No provider components yet. EKS Auto Mode comes first, self-hosted Talos
  second; each is a separate entry here when it lands.
