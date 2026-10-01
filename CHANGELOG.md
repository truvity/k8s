# Changelog

The first release is v0.1.0 (its heading carries no date until the tag is cut).
Entries are written for someone deciding whether to bump: what changed for
them, and for anything breaking, what to do.

## v0.2.0

- `pkg/aws/backupbucket`: the cross-account backup bucket as a component,
  `truvity:k8s/aws:BackupBucket`. A versioned bucket in a backup account
  with its own KMS key (rotation on, retained on delete), a bucket policy
  that admits a named writer role, an optional read-only role and list-only
  roles and denies `s3:DeleteObjectVersion` outside the backup account, a
  caller-supplied lifecycle (`Backstop` builds the common rule), optional
  COMPLIANCE Object Lock, and an optional same-account replica in another
  region with its replication role. Names, tags, key descriptions, the
  replication role's permissions boundary and the partition are inputs; none
  has an estate default. The bucket, the replica bucket and both keys are
  protected unless `Protect` points at false. `LegacyTopLevel` adopts loose
  resources by alias, including the SDK's former-type aliases re-expressed
  with no parent, so a state that still holds the pre-v2 type resolves.

## v0.1.0

- The provider-neutral cluster contract (`pkg/cluster`): `Outputs` (name,
  endpoint, certificate authority, OIDC issuer, capabilities), the six
  capabilities (node pools, storage, network policy, workload identity, load
  balancing, API access) and `Outputs.Validate`, which refuses an outputs
  value a consumer could not safely use and reports every problem at once.
- `pkg/aws/pullthroughcache`: the ECR pull-through cache as a component,
  `truvity:k8s/aws:PullThroughCache`. One rule per upstream registry, and for
  an upstream that needs credentials a Secrets Manager secret (under the
  `ecr-pullthroughcache/` name prefix ECR requires) and its version. The
  caller supplies the credentials as Pulumi string inputs and may supply a
  naming hook; `LegacyTopLevel` aliases every child from its URN as a loose
  resource under the stack, so existing resources are adopted in place. It
  reports the prefixes so the caller can grant the pull permissions.
