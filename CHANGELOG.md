# Changelog

The first release is v0.1.0 (its heading carries no date until the tag is cut).
Entries are written for someone deciding whether to bump: what changed for
them, and for anything breaking, what to do.

## Unreleased

- `charts/cluster-foundation`: the objects a cluster needs before any workload,
  as data, published to `oci://ghcr.io/truvity/charts/cluster-foundation` from
  the next tag. `namespaces` renders one whole `Namespace` per entry with its
  Pod Security labels (level, version and every mode are required values; an
  entry that departs from the default level must carry a `reason`; a mode list
  without `enforce` is refused unless `podSecurity.unenforced` says it is on
  purpose) and `argocd.argoproj.io/sync-options: Prune=false,Delete=false`
  (`protect`, on by default, overridable per entry). `clusterRoleBindings` binds
  groups, users or service accounts to a ClusterRole. `storageClasses` renders
  StorageClasses, one of them optionally the default. `eksAuto.networkPolicyController`
  renders the `amazon-vpc-cni` ConfigMap that turns the EKS Auto Mode
  NetworkPolicy controller on. It is for the tool that OWNS a namespace;
  `cluster-baseline` stays the labels-only chart for namespaces someone else
  owns. Nothing is defaulted to an estate: every list is empty.
- `charts/eks-auto-node-pools`: Karpenter `NodePool`s and the EKS Auto Mode
  `NodeClass`es they need, published to
  `oci://ghcr.io/truvity/charts/eks-auto-node-pools` from the next tag. Pools
  take architectures, capacity types, instance categories, taints, limits, zone
  pins and expiry; one disruption budget per pool keeps consolidation to a node
  at a time. Subnet tags, the node role and the cluster name are values; the
  built-in `default` class is never rendered. `protect` (on by default) marks
  every object so Argo CD neither prunes nor deletes it.
  `just lint` and `just test` cover both charts (schema, 18 and 15 refused
  fixtures, goldens).

## v0.9.0

- `charts/volume-snapshot-crds` (the CSI external-snapshotter CRDs) and `charts/cilium-crds` (the Cilium CRDs) move here from `truvity/ocictl` and are published from here (`oci://ghcr.io/truvity/charts/<name>`) at the UPSTREAM version they mirror, as they were from `truvity/ocictl`: `8.6.0` and `1.20.1` today, both already in the registry, so the first release here publishes nothing for them. A release publishes a chart only when its version is not in the registry yet, and never overwrites one. The rendered CRDs are byte-identical to the ocictl charts. The CRDs are generated from each `crdctl.yaml` by `just crds` (crdctl from a pinned `truvity/ocictl` release) and committed; `just crds-check` fails when they drift from the pinned upstream.

## v0.8.0

- `charts/cluster-baseline`: the Pod Security Admission baseline as a Helm
  chart, published to `oci://ghcr.io/truvity/charts/cluster-baseline` from the
  next tag. It renders one labels-only `Namespace` per entry of
  `podSecurity.namespaces`, to be applied server-side so it owns the PSA labels
  and nothing else. The defaults are warn-first: every listed namespace is
  warned and audited at `restricted`, nothing is enforced. A namespace may
  override its level, or one label kind, only with a `reason`, which the render
  enforces and records as an annotation. Version pinning per mode, an
  enforce canary per namespace, and `protect` (on by default: Helm keeps and
  Argo CD neither prunes nor deletes a namespace that leaves the list). The
  list of namespaces is empty by default: the estate names its own.
  `just lint` and `just test` now cover the chart (schema, ten refused
  fixtures, four golden renders); the devbox pins `kubernetes-helm`.
- `charts/cluster-baseline`: a guard, and three extras, all off until switched
  on. `guard.enabled` renders a `ValidatingAdmissionPolicy` and binding that
  warn and audit (`validationActions`, `Deny` allowed later;
  `failurePolicy: Ignore`) when a namespace is created, or updated to drop its
  labels, with no Pod Security label; the control-plane namespaces are
  excluded, the list is configurable. Per listed namespace, opt-in:
  `networkPolicy` (default-deny ingress and/or egress; a `dnsEgress` decision
  is required when egress is denied; configurable name, to avoid duplicating
  an owner's deny policy), `resourceQuota` (values only; a `reason` or an
  `owner` required) and `limitRange` (container defaults and bounds; a `reason`
  or an `owner` required, as for quotas, enforced by the schema and the
  render). Each has a `protect` switch, per kind and per entry. Only the
  default-deny policy is protected by default (Helm keeps it, Argo CD neither
  prunes nor deletes it): an accidental prune of it is silent, whereas
  removing a quota or a limit range only relaxes a limit. No
  cluster-wide baseline floor is offered; namespaces move to `restricted` one
  at a time. 16 more refused fixtures and three more golden renders.

## v0.7.0

- `pkg/aws/eksoidc`: the external OIDC issuer association of an EKS cluster
  as a component, `truvity:k8s/aws:EksOidc`: one `eks.IdentityProviderConfig`
  with the issuer URL (https), the client id (the audience) and the
  association's name. The username comes from `sub` with no prefix (`-`) and
  the groups from `groups` unless `UsernameClaim`, `UsernamePrefix` or
  `GroupsClaim` say otherwise. EKS admits one such association per cluster, so
  the child always registers with delete-before-replace. It refuses a missing
  provider or cluster name, an empty client id, association name or issuer URL,
  an issuer URL that is not https and a naming hook that returns an empty name,
  all reported at once. The child's name comes from a caller-supplied hook. It
  is protected unless `Protect` points at false: replacing it is a 10 to 15
  minute cluster update with OIDC sign-in down. `LegacyTopLevel` adopts a loose
  association by alias.

## v0.6.0

- `pkg/aws/ekscluster`: an EKS Auto Mode cluster as a component,
  `truvity:k8s/aws:EksCluster`. The cluster, the KMS key its secrets are
  encrypted with (rotation always on, 365 days by default) and its alias, the
  cluster role and the node role with their managed policies (the caller
  passes the ARNs) and the node role's inline policies, and the control-plane
  log group (90 days by default). The shape is fixed: API authentication mode,
  private-only endpoint unless `PublicEndpoint`, no bootstrap creator-admin,
  no self-managed add-ons, EKS-managed compute, load balancing and block
  storage. The cluster comes back as `Cluster` for the access entries, rules
  and add-ons the caller attaches. Trust documents default to the EKS and EC2
  services and `ClusterTrustPolicy` / `NodeTrustPolicy` replace them
  verbatim. It refuses a missing provider, name, version, subnet or service
  CIDR, a service CIDR that is not IPv4, an empty or repeated subnet, policy
  or inline policy, no cluster policy, one name for both roles, an unknown log
  type, a key rotation period outside 90 to 2560 days and a naming hook that
  returns an empty or repeated name, all reported at once. Every child's name
  comes from a caller-supplied hook. The cluster, the key and both roles are
  protected unless `Protect` points at false. `LegacyTopLevel` adopts loose
  resources by alias.

## v0.5.0

- `pkg/aws/podidentity`: an EKS Pod Identity role as a component,
  `truvity:k8s/aws:PodIdentity`. One IAM role, its trust policy and one
  `PodIdentityAssociation` per service account; the permissions are the
  caller's (an inline role policy, a managed policy the component creates and
  attaches, or both). The default trust policy is always scoped to the
  cluster (source account and source ARN) and to the namespace and service
  accounts; `TrustPolicy` replaces it verbatim, for adopting a role whose
  document was rendered another way. It refuses a missing provider or
  cluster, an empty role name or namespace, no service account or a repeated
  one, no way to render the trust, a permission with no name or document, an
  import for an unlisted service account and a naming hook that returns an
  empty or repeated name, all reported at once. Every child's name comes from
  a caller-supplied hook; there is no estate default. Nothing is protected
  unless `Protect` is true. `Imports` adopts an existing role, policy,
  attachment or association by ID, and `IgnoreAssociationTags` leaves the
  tags another tool set. `LegacyTopLevel` adopts loose resources by alias.

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
