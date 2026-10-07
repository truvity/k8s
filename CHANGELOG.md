# Changelog

The first release is v0.1.0 (its heading carries no date until the tag is cut).
Entries are written for someone deciding whether to bump: what changed for
them, and for anything breaking, what to do.

## Unreleased

## v0.24.0

- `pkg/aws/ekscluster`: `Args.AccessEntries` (one `STANDARD` access entry and one access policy association per principal, cluster- or namespace-scoped) and `Args.CoreDNS` (the coredns add-on; its Corefile is `StockCorefile` of `DefaultCoreDNSVersion`, or the caller's, with the `Extras` lines inserted above the `kubernetes` stanza). New child kinds `access-entry`, `access-policy`, `coredns`, default names `<c>-access-<name>`, `<c>-access-<name>-policy`, `<c>-coredns`; with `LegacyTopLevel` they carry the no-parent alias like every other child, so a stack that registered them loose adopts them with no change. Neither set: nothing changes.

## v0.23.0

- `pkg/nscatalog`: `Build` derives one cluster's catalog from its facts (`Inputs`: the Pod Security table, the foundation's namespaces, the tenant inventory, the business projects, the listener groups' selectors, the shared broker, the delivery projects, Kargo). Every project, CI and employee namespace is written by `guardrails-projects`, which stamps its Pod Security labels and renders its ACK role selector; the label keys are the caller's (`LabelKeys`, with `Identity`). The tenancy naming (`EmployeeNamespace`, `CIRepoNamespace`, `Tenancy.Namespaces`) comes with it. Moved from a consumer; its catalogs are unchanged.
- `pkg/cluster`: the Pod Security table (`PodSecurity`: `Validate`, `Resolve` to what a Namespace's owner stamps per row, the same labels `cluster-baseline.resolve` renders; `WithDeletableRows`, `ResolvedPodSecurity.WithOwnedRow`) and the label guard (`DeriveLabelGuard`, `LabelGuard.Validate`: who may write the protected namespace labels). Moved from a consumer.
- `charts/cluster-baseline`: the schema refuses a namespace whose `level` departs from `podSecurity.level` without a `reason` (the template already did; a schema error names the namespace before anything renders).

## v0.22.0

- `pkg/aws/ackfactory`: a project's ACK role can list KMS aliases and create, update and delete aliases named `alias/*-<project>-*` (an alias ARN carries no tags, so the tag condition cannot cover it). Using an alias still needs the project-tagged key.

## v0.21.0

- `pkg/aws/vpclayout`: the address plan of a fleet of VPCs as arithmetic over declared facts: `Plan.VPCCIDR`, `DNSIP`, `PublicSubnetCIDR`, `Calculate` (each logical subnet cut into one block per AZ, /24 slots in a /22 or larger per-AZ blocks, overlaps and misalignment refused), `FilterAZs` (a region's AZs by slot index) and the `SubnetName` / `AZSuffix` naming of a subnet's Name tag. Moved from a consumer; the CIDRs and names are unchanged. The output is what `pkg/aws/vpc` takes as `Args`.

## v0.20.0

- `pkg/cluster`: the EKS Auto Mode node pool derivation (`DeriveNodePools`, `DeriveNodeClasses`, `ApplyDefaultNodeClass`: the `eks-auto-node-pools` chart's `nodePools` and `nodeClasses` from declared pools, with the taints, the node role name and gp3's baseline filled in) and the input rules of a declared pool (`ValidateNodePool`, `ValidateNodeClass`, `ValidateEphemeralStorage`). A pool that states no architecture, capacity type or instance category takes the caller's `PoolDefaults`; the library has none of its own.
- `charts/eks-auto-node-pools`: the schema refuses what the NodeClass or Karpenter would refuse later: an instance category outside c, m, r, t; `maxInstanceCpu` of 1 (Auto Mode admits no instance at or below 1 vCPU); `iops` above 16000; `throughput` above 1000. Four refusal fixtures.

## v0.19.0

- `pkg/nscatalog`: the namespace catalog contract (one row per cluster and namespace, one column group per concern), its YAML encoding (`Marshal` takes the provenance header the caller stamps; `Unmarshal`, `MarshalUnder`) and the read-only live check (`Compare`, with `ManagedByDomains`, `CompareACKSelectors`, the kubectl JSON parsers). Moved from a consumer; no behaviour change.

## v0.18.0

- `charts/guardrails-projects`: `projects[].podIdentities` (names under
  `podIdentities.associations`) renders a ServiceAccount in the row's
  namespace and an ACK PodIdentityAssociation `<namespace>-<name>` in the
  association's own namespace. Nothing renders without it.

## v0.17.0

- `pkg/aws/ackfactory`: `ProjectRolesOptions.ForbiddenRoles`, role-name patterns a project role may not touch (an explicit `iam:*` deny, so it wins over the project-prefixed allow): for a role the platform's own identity mints for a project, such as an archive role, whose name contains the project's name. Empty: the policy is unchanged.

## v0.16.0

- `charts/guardrails-projects` (new, published from the next tag): the
  guardrails of every project namespace from a list of project rows, for an
  L3 `-projects` Application: Namespace with labels and Pod Security,
  baseline NetworkPolicy, quota and LimitRange by profile, Roles and
  RoleBindings, an ACK IAMRoleSelector per ceiling, shared ClusterRoles.
  Namespaces and selectors carry `Prune=false,Delete=false`.

## v0.15.0

- `charts/cluster-baseline`: the per-namespace guardrails kit, all opt-in:
  a namespace's own `labels` beside its Pod Security labels; in the
  default-deny NetworkPolicy, `intraNamespace` and `ingress`/`egress` allow
  rules (verbatim, each with a `description`); `rbac` (namespaced Roles and
  RoleBindings, ClusterRoles with `aggregateTo`); `ackRoleSelectors` (one ACK
  IAMRoleSelector per namespace, by name, protected by default). A release
  that sets none of them renders exactly what the previous one did.
- `charts/cluster-baseline`: `podSecurity.namespaces.<ns>.modes.<kind>.enabled:
  false` switches one label kind off for one namespace (a warn-first namespace
  on an enforcing cluster); like any departure from the defaults it needs a
  `reason`.

## v0.14.0

- `charts/cluster-baseline`: `labelGuard`, a ValidatingAdmissionPolicy that
  lets only named principals (`allowedUsers`, `allowedUserPrefixes`,
  `allowedGroups`) set, change or remove protected namespace labels
  (`protectedPrefixes`, default the Pod Security labels; `protectedDomains`,
  a domain and its subdomains). Off by default, `[Warn, Audit]` when on: a
  release that does not set it renders exactly what the previous one did.

## v0.13.0

- **Deprecated:** `pkg/aws/backupbucket` moves to `github.com/truvity/cnpg/v2/pkg/aws/backupstore` (K5: the backup bucket belongs to the CNPG family) and is now a forwarder of type aliases and one function, for one release. Importers keep compiling; to migrate, change the import path and `BackupBucket` to `BackupStore`. The component's Pulumi type is now `truvity:cnpg/aws:BackupStore`, aliased from `truvity:k8s/aws:BackupBucket`, so a stack that deployed it from here previews with no change and no replace. k8s now depends on `truvity/cnpg/v2`. The package is removed in the release after next.

## v0.12.0

- `pkg/aws/ackfactory`: the AWS identity of the ACK controllers, lifted from a
  cluster program: `IAM` and `EKS` (the roles of the controllers that mint IAM
  roles and Pod Identity associations), `Services` (the s3, kms and dynamodb
  controllers' roles, adopting a live policy, role or association through a
  `Lookup` the caller implements, so no cloud SDK is added) and `ProjectRoles`
  (one capability role per project, under a boundary the caller names). Plain
  functions, not a component: the same resources under the same logical names,
  so a stack that registered them itself previews no change. Cluster,
  account, region, partition, boundary names and projects are inputs.

- `charts/cluster-network-policies`: a cluster's own NetworkPolicies as data,
  published to `oci://ghcr.io/truvity/charts/cluster-network-policies` from the
  next tag. One `NetworkPolicy` per entry of `policies`: name, namespace, pod
  selector, policy types and the rules verbatim, with a sync wave and optional
  Argo CD deletion protection. Every entry needs a `reason`; the render refuses
  rules for a direction the policy does not list, a duplicate and an unknown
  key. The chart renders the shape, the caller says what is admitted; it never
  renders a Namespace. `just lint` and `just test` cover it (schema, 13
  refused fixtures, goldens).

- `charts/tenancy` is deprecated (`deprecated: true` in `Chart.yaml`, so Helm
  prints a notice on install and pull). Nothing is removed and what the chart
  renders does not change. Tenancy is moving to guidelines (a contract in
  `truvity/policy`), thin per-tenant values and a per-tenant identity
  ServiceAccount; the generic objects already have homes in `cluster-foundation`,
  `cluster-baseline` and `cluster-network-policies`. The window, which ends on
  conditions and not a date, and what the chart gets until then (fixes only) are
  in `docs/decisions/0004-tenancy-deprecation.md`. If you install this chart: no
  action now; plan the move before the window ends.

## v0.11.0

- `charts/tenancy`: renders each tenant's `Namespace`, with its Pod Security
  labels and deletion protection, where the tenant's profile (or the tenant)
  carries a `namespace` map; a tenant with `namespace: false` is a namespace
  someone else renders. `podSecurity` (`level`, `version`, `modes`) is required
  once one is rendered; a `namespace` may say its own `level` (a different one
  needs a `reason`, carried in the annotation `podSecurity.reasonAnnotation` names) and `modes` (a subset of `warn`, `audit`, `enforce`, no
  repeats, at least one). A list of modes without `enforce` is a warn-first
  rollout and is refused unless `unenforced: true` (on the entry or profile, or
  `podSecurity.unenforced`) says it is on purpose. `protect` has a `namespace`
  feature (profile or entry), and `syncWaves.namespaces` orders them. Nothing
  changes for a values file that does not use it. This supersedes the
  per-namespace `modes` proposed for `cluster-foundation`: the tenant
  namespaces move here, and `cluster-foundation` keeps the cluster's own.
  `just lint` no longer forbids a Namespace in the tenancy goldens; one golden
  case (profiles that enforce, warn-first and opt out) and 16 refused fixtures
  cover it. `Chart.yaml`, `values.yaml` and `docs/reference.md` describe it.

## v0.10.0

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
- `charts/tenancy`: the per-tenant plumbing of a shared cluster, published to
  `oci://ghcr.io/truvity/charts/tenancy` from the next tag. For each listed
  tenant namespace it renders a baseline `NetworkPolicy` (default-deny both
  ways plus ordered rules that may be limited to some profiles), a
  `ResourceQuota` and `LimitRange`, namespaced `Role`s and `RoleBinding`s, a
  NATS account (the NACK `ServiceAccount`, its token `Secret` and the `Account`)
  and `ServiceAccount`s with their EKS `PodIdentityAssociation`s, which live in
  a namespace the tenant cannot edit; and cluster-wide `ClusterRole`s that can
  aggregate into `admin`, `edit` or `view`. Tenants share a profile (tier) that
  carries the policy, quota, labels and per-feature protection. It never
  renders a Namespace: that is `cluster-foundation`'s object. `protect`
  (`true`, `"prune-only"` or `false`, per feature and per entry) controls the
  Argo CD `sync-options`. A tenant that names a missing profile, a binding to a
  Role the tenant does not have, NATS without servers or a pod identity that is
  not declared is refused. Nothing is defaulted to an estate. `just lint` and
  `just test` cover it (schema, 37 refused fixtures, goldens).

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
