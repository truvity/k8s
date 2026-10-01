# Changelog

The first release is v0.1.0 (its heading carries no date until the tag is cut).
Entries are written for someone deciding whether to bump: what changed for
them, and for anything breaking, what to do.

## v0.4.0

- `pkg/aws/vpc`: a VPC as a component, `truvity:k8s/aws:Vpc`. The caller
  states the IPv4 CIDR, the availability zones and the logical subnets (one
  CIDR per zone each, a public or private type, an explicit IPv6 /64 index);
  the component adds the internet gateway and public route table, one private
  route table per zone, IPv6 (an Amazon-provided /56, a /64 per subnet, an
  egress-only gateway; `DisableIPv6` turns it off), NAT egress for the private
  route tables (`NAT`: one gateway by default, one per zone, or none), S3 and
  DynamoDB gateway endpoints (`GatewayEndpoints`), an emptied default security
  group, a restrictive default network ACL (with `DefaultNACLHTTPSIngress` for
  peered VPCs; `KeepDefaultSecurityGroup` and `KeepDefaultNACL` opt out) and
  CloudWatch flow logs with a 365-day default retention (`FlowLogs`). It
  refuses a bad CIDR, subnets outside the VPC or overlapping, a subnet that
  does not span exactly the listed zones, colliding or out-of-range IPv6
  indexes, a NAT with no public subnet, a missing provider and a naming hook
  that returns an empty or repeated name, all reported at once. Every child's
  name comes from a caller-supplied hook, and tags can be set per child; there
  is no estate default. The VPC and the subnets are protected unless `Protect`
  points at false. The default network ACL is owned whole by the component:
  a rule added to it elsewhere is deleted by the next apply. `LegacyTopLevel`
  adopts loose resources by alias.

## v0.3.0

- `pkg/aws/vpcpeering`: a VPC peering as a component,
  `truvity:k8s/aws:VpcPeering`. A connection requested from one side and
  accepted from the other (accounts and regions differ: the caller supplies
  one AWS provider per side), a route to the other VPC's CIDR in every route
  table listed on each side (IPv6 too when both IPv6 CIDRs are given),
  optional DNS-resolution options per side and optional private hosted-zone
  associations (cross-account ones authorize first). It refuses overlapping
  IPv4 CIDRs, a missing provider, a side with no route table and a half
  IPv6 pair, all reported at once. Every child's name comes from a
  caller-supplied hook; there is no estate default. It creates no network
  ACL or security group rules. The connection and its accepter are protected
  unless `Protect` points at false. `LegacyTopLevel` adopts loose resources
  by alias.

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
