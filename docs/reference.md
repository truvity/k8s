# Reference

## `pkg/cluster`

### `Outputs`

| Field | Type | Meaning |
| --- | --- | --- |
| `Provider` | `Provider` | The kind of component that made the cluster (`eks`, `talos`). Informational. |
| `Name` | `string` | The cluster's name, as the caller supplied it; a DNS-1123 label. |
| `Endpoint` | `string` | The API server address, an `https` URL. |
| `CertificateAuthorityPEM` | `string` | The PEM bundle to trust the API server against; PEM text, not base64. |
| `OIDCIssuer` | `string` | The `https` URL that signs ServiceAccount tokens. Required with `WorkloadIdentity`, empty otherwise. |
| `Capabilities` | `Capabilities` | The set of optional abilities the cluster offers. |

`Outputs.Validate()` returns nil, or one error that joins every problem.

### Capabilities

| Capability | Constant | A cluster that offers it guarantees |
| --- | --- | --- |
| `node-pools` | `NodePools` | Capacity can be declared in groups with their own shape, and scales on demand. |
| `storage` | `Storage` | A default StorageClass provisions persistent volumes dynamically. |
| `network-policy` | `NetworkPolicy` | NetworkPolicy objects are enforced, not merely accepted. |
| `workload-identity` | `WorkloadIdentity` | A pod's ServiceAccount can be exchanged for credentials outside the cluster. |
| `load-balancing` | `LoadBalancing` | A LoadBalancer Service or a Gateway gets an externally reachable address. |
| `api-access` | `APIAccess` | The API server is reachable from the places the caller named, at `Endpoint`. |

`All()` lists them in a stable order. `NewCapabilities`, `Has` and `List`
build, query and enumerate a set. A capability this version does not define
is refused by `Validate`.

## `pkg/aws/pullthroughcache`

`truvity:k8s/aws:PullThroughCache` deploys Amazon ECR pull-through cache
rules. It needs an AWS provider: pass it with `pulumi.Providers(p)`.

```go
cache, err := pullthroughcache.NewPullThroughCache(ctx, "cache", &pullthroughcache.Args{
	Upstreams: []pullthroughcache.Upstream{
		{Prefix: "hub", RegistryURL: "registry-1.docker.io", NeedsCredentials: true},
		{Prefix: "quay", RegistryURL: "quay.io"},
	},
	Credentials: map[string]pullthroughcache.Credentials{
		"hub": {Username: hubUser, Token: hubToken}, // pulumi.StringInput
	},
}, pulumi.Providers(awsProvider))
// cache.PrefixList() / cache.Prefixes: grant the pullers
// ecr:BatchImportUpstreamImage and ecr:CreateRepository on these prefixes.
```

### `Args`

| Field | Type | Meaning |
| --- | --- | --- |
| `Upstreams` | `[]Upstream` | The registries to cache; at least one. `Prefix` (2 to 30 characters ECR accepts, unique), `RegistryURL` (host, no scheme), `NeedsCredentials`. |
| `Credentials` | `map[string]Credentials` | By `Prefix`: `Username` and `Token` (`pulumi.StringInput`). Required for every upstream with `NeedsCredentials`, refused for any other prefix. The component reads no parameter store. |
| `Names` | `NameFunc` | Optional hook returning the logical name of a child for a `Kind` (`rule`, `secret`, `secret-version`) and prefix. Nil uses `DefaultName`. Names must be non-empty and unique. |
| `LegacyTopLevel` | `bool` | Alias every child from the URN it has as a loose resource directly under the stack. For adopting existing resources. |

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: no upstreams, an empty, malformed
or duplicate prefix, an empty registry URL, a credentialed upstream without
both credentials, credentials for an upstream that takes none, an empty or
repeated child name.

### Children

For each upstream, with the default names:

| Child | Type | Name | Present when |
| --- | --- | --- | --- |
| rule | `aws:ecr/pullThroughCacheRule:PullThroughCacheRule` | `ptc-rule-<prefix>` | always |
| secret | `aws:secretsmanager/secret:Secret` | `ptc-secret-<prefix>` | `NeedsCredentials` |
| secret version | `aws:secretsmanager/secretVersion:SecretVersion` | `ptc-secret-version-<prefix>` | `NeedsCredentials` |

The Secrets Manager secret is named `ecr-pullthroughcache/<prefix>`, a prefix
ECR requires, in the default `aws/secretsmanager` key (ECR accepts no
customer key); its payload is `{"username": ..., "accessToken": ...}` and is
marked secret. The rule carries the secret's ARN and depends on the secret
and its version, because ECR treats a secret with no version as not found.

With `LegacyTopLevel` every child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the same
name and type, no parent. The AWS provider resource is not a child and is
not aliased.

### Outputs

`Prefixes` (`pulumi.StringArrayOutput`) and `PrefixList()` (`[]string`): the
local repository prefixes, in the order of `Upstreams`.

## `pkg/aws/backupbucket`

`truvity:k8s/aws:BackupBucket` deploys a cross-account backup bucket: a
versioned S3 bucket in a backup account that a workload account may write to
and may never permanently delete from. It needs an AWS provider for the backup
account: pass it with `pulumi.Providers(p)`.

```go
b, err := backupbucket.New(ctx, "backup", &backupbucket.Args{
	BucketName:      "example-backup",
	SourceAccountID: sourceAccountID,
	BackupAccountID: backupAccountID,
	WriterRoleName:  "example-writer-*",
	KeyDescription:  "example backups example-backup",
	Tags:            map[string]string{"Owner": "platform"},
	LifecycleRules: backupbucket.Backstop(backupbucket.BackstopArgs{
		ExpireDays: 180, NoncurrentDays: 180,
		Transitions: []backupbucket.Transition{{Days: 30, StorageClass: "STANDARD_IA"}},
	}),
}, pulumi.Providers(backupAccountProvider))
// b.BucketName, b.BucketARN, b.KMSKeyARN: grant the writer from these.
```

### `Args`

| Field | Type | Meaning |
| --- | --- | --- |
| `BucketName` | `string` | The bucket's name; also the stem of every child's logical name. Required. |
| `SourceAccountID`, `BackupAccountID` | `string` | The account whose roles write, and the account the bucket lives in. Only principals in the backup account may delete an object version. Required. |
| `Partition` | `string` | The AWS partition in ARNs. Empty is `aws`. |
| `WriterRoleName` | `string` | The role in the source account the bucket and key policies admit. A glob (`*`, `?`) is allowed. Required. |
| `WriterObjectActions` | `[]string` | Replaces the writer's object actions. Nil grants put, get, delete and the two multipart actions; an append-only log passes put and get. Empty is refused. |
| `ReaderRoleName` | `string` | One more role in the source account, read-only (get, list, decrypt). An exact name; a glob is refused. |
| `ListerRoleNames` | `[]string` | Roles that may list the bucket (and the replica) and nothing else: no object, no decrypt. Exact names; a glob or empty name is refused. |
| `KeyDescription` | `string` | The KMS key's description. Required, and part of the key's state: a change is an in-place update. |
| `Tags` | `map[string]string` | Tags on the bucket, for example compliance tags. The component adds none. |
| `LifecycleRules` | `s3.BucketLifecycleConfigurationV2RuleArray` | The lifecycle configuration; at least one rule. `Backstop` builds one whole-bucket rule. |
| `ObjectLockDays` | `int` | Above zero: Object Lock with a COMPLIANCE default retention of that many days, on the bucket and the replica. Negative is refused. |
| `Replica` | `*Replica` | Replicates to another region; see below. |
| `Protect` | `*bool` | Protect the buckets and the KMS keys. Nil means true; point it at false only to retire a bucket. |
| `LegacyTopLevel` | `bool` | Alias every child from the URN it has as a loose resource directly under the stack. For adopting existing resources. |

`Replica` takes `Provider` (the AWS provider of the replica's region, created
by the caller so it keeps its own URN), `BucketName`, `KeyDescription`,
`RoleName` (the replication role's IAM name) and an optional
`RolePermissionsBoundary` (`pulumi.StringInput`; nil sets none). The
replica gets a bucket policy only when `ListerRoleNames` is not empty.

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: a missing required field, no
lifecycle rules, a negative lock, a glob in a reader or lister name, an
empty list of writer actions, a replica without a provider, name, key
description or role name, or a replica named as the primary.

### Children

Logical names derive from the bucket name `<b>` and, for the replica, the
replica bucket name `<r>`. They are API.

| Child | Type | Name | Present when |
| --- | --- | --- | --- |
| key | `aws:kms/key:Key` | `<b>-kms` | always (protected, retained on delete) |
| key alias | `aws:kms/alias:Alias` | `<b>-kms-alias` | always |
| bucket | `aws:s3/bucket:Bucket` | `<b>` | always (protected) |
| versioning | `aws:s3/bucketVersioningV2:BucketVersioningV2` | `<b>-versioning` | always |
| encryption | `aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2` | `<b>-encryption` | always |
| lifecycle | `aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2` | `<b>-lifecycle` | always |
| object lock | `aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2` | `<b>-object-lock` | `ObjectLockDays > 0` |
| public access block | `aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock` | `<b>-public-access` | always |
| bucket policy | `aws:s3/bucketPolicy:BucketPolicy` | `<b>-policy` | always |
| replica key | `aws:kms/key:Key` | `<r>-kms` | `Replica` (protected, retained on delete) |
| replica bucket | `aws:s3/bucketV2:BucketV2` | `<r>` | `Replica` (protected) |
| replica versioning | `aws:s3/bucketVersioningV2:BucketVersioningV2` | `<r>-versioning` | `Replica` |
| replica public access block | `aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock` | `<r>-public-access` | `Replica` |
| replica policy | `aws:s3/bucketPolicy:BucketPolicy` | `<r>-policy` | `Replica` and `ListerRoleNames` |
| replication role | `aws:iam/role:Role` | `<b>-replication-role` | `Replica` |
| replication role policy | `aws:iam/rolePolicy:RolePolicy` | `<b>-replication-policy` | `Replica` |
| replication | `aws:s3/bucketReplicationConfig:BucketReplicationConfig` | `<b>-replication` | `Replica` |

The replica's children use the replica provider; every other child uses the
provider the component was given. Every key is retained on delete: key
destruction is a break-glass act in the accounts this is built for.

### Aliases

With `LegacyTopLevel` every child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the same
name and type, no parent.

The AWS SDK also aliases some of its own types from the type they had before
the v2 split: the bucket from `aws:s3/bucketV2:BucketV2` (twice, an
identical pair), and versioning, encryption, lifecycle and object lock each
from their own type. The SDK declares those with no parent, so under a
component they resolve beneath the component, not the stack. To keep a state
that still holds the former type at the top level resolvable, `LegacyTopLevel`
adds, for each of those children, one alias with that former type and no
parent. The SDK's identical pair on the bucket resolves to one URN, so the
component declares it once. A test fails when an SDK upgrade declares an
alias the component does not mirror.

### Outputs

`BucketName`, `BucketARN`, `KMSKeyARN` (`pulumi.StringOutput`). The component
exports no stack output of its own: the caller names those.

## `pkg/aws/vpcpeering`

`truvity:k8s/aws:VpcPeering` deploys one VPC peering between two VPCs that
may be in different accounts and regions: the connection, its acceptance, the
routes, and optionally DNS-resolution options and private hosted-zone
associations. It takes one AWS provider per side in `Args`
(`RequesterProvider`, `AccepterProvider`), never from `pulumi.Providers`: a
default provider would put the accepter's resources in the requester's
account.

```go
p, err := vpcpeering.New(ctx, "hub-spoke", &vpcpeering.Args{
	RequesterVPCID:       hubVPC,
	AccepterVPCID:        spokeVPC,
	AccepterAccountID:    spokeAccountID,
	RequesterCIDR:        "10.64.0.0/16",
	AccepterCIDR:         "10.65.0.0/16",
	RequesterRouteTables: map[string]pulumi.StringInput{"a": hubRTA, "public": hubRTPublic},
	AccepterRouteTables:  map[string]pulumi.StringInput{"a": spokeRTA, "public": spokeRTPublic},
	RequesterProvider:    hubProvider,
	AccepterProvider:     spokeProvider,
	Names: func(c vpcpeering.Child) string { /* the names your stack already uses */ },
})
// p.ConnectionID: export it under the name you choose.
```

### `Args`

| Field | Meaning |
| --- | --- |
| `RequesterVPCID`, `AccepterVPCID` | The two VPCs. Required. |
| `AccepterAccountID` | Owner of the accepter VPC (`PeerOwnerId`). Required. |
| `AccepterRegion` | Accepter VPC region, for a cross-region peering. Empty: the requester's. |
| `RequesterCIDR`, `AccepterCIDR` | Primary IPv4 CIDRs. Required, must not overlap. |
| `RequesterIPv6CIDR`, `AccepterIPv6CIDR` | Both for IPv6 routes, or neither. |
| `RequesterRouteTables`, `AccepterRouteTables` | Route tables by caller-chosen key (an AZ name, `"public"`). At least one per side. |
| `RequesterProvider`, `AccepterProvider` | AWS providers of the two accounts and regions. Required. |
| `RequesterDNSResolution`, `AccepterDNSResolution` | `*bool`: `AllowRemoteVpcDnsResolution` for that side. Nil: no options child for it. |
| `ZoneAssociations` | Private hosted zones to associate with a VPC (`Name`, `ZoneID`, `VPCID`, `Region`, `CrossAccount`, `ZoneProvider`, `VPCProvider`). Cross-account ones need both providers and register an authorization first. |
| `Tags` | Set on the connection and the accepter. |
| `Names` | Naming hook, `func(Child) string`. Nil: `DefaultName`. |
| `LegacyTopLevel` | Adopt loose resources by alias. |
| `Protect` | `*bool`; nil means true. |

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: overlapping or non-IPv4 CIDRs, a
missing provider, VPC or account, a side with no route table (or a nil one),
a half-specified IPv6 pair, a zone association with no name, zone, VPC,
region or provider, and a naming hook that returns an empty or repeated
name.

The component creates no network ACL rules and no security group rules. A
caller that owns a VPC's default network ACL owns its whole rule list; admit
cross-VPC traffic where the ACL and the groups are defined.

### Children

`Names` receives a `Child` (`Component`, `Kind`, `Side`, `Family`, `Key`) and
returns the logical name; the defaults below use the component name `<c>`.
Names are API.

| Child | Type | Default name | Present when |
| --- | --- | --- | --- |
| connection | `aws:ec2/vpcPeeringConnection:VpcPeeringConnection` | `<c>-connection` | always (protected; requester provider) |
| accepter | `aws:ec2/vpcPeeringConnectionAccepter:VpcPeeringConnectionAccepter` | `<c>-accepter` | always (protected; accepter provider) |
| route | `aws:ec2/route:Route` | `<c>-route-<side>-<family>-<key>` | one per route table per side and family (`ipv4`; `ipv6` when both IPv6 CIDRs are set); a requester route sends the accepter CIDR to the connection and the reverse; depends on the accepter |
| DNS options | `aws:ec2/peeringConnectionOptions:PeeringConnectionOptions` | `<c>-dns-<side>` | that side's `*DNSResolution` is set |
| zone authorization | `aws:route53/vpcAssociationAuthorization:VpcAssociationAuthorization` | `<c>-zone-<name>-auth` | `ZoneAssociations[].CrossAccount` (`ZoneProvider`) |
| zone association | `aws:route53/zoneAssociation:ZoneAssociation` | `<c>-zone-<name>-assoc` | one per `ZoneAssociations` entry (`VPCProvider`) |

Requester-side children use `RequesterProvider`, accepter-side children
`AccepterProvider`. A same-account zone association uses `VPCProvider`, else
`ZoneProvider`, else the requester provider.

### Aliases

With `LegacyTopLevel` every child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the name the
hook gives it, the same type, no parent. The AWS SDK declares no aliases of
its own on these types today (ADR 0003 item 6); a test fails when an upgrade
adds one the component does not mirror.

### Protection

The connection and its accepter are protected unless `Protect` points at
false: deleting the connection cuts all traffic between the VPCs, so a
preview that would delete or replace it fails instead. Point `Protect` at
false only for a peering that is meant to be torn down.

### Outputs

`ConnectionID` (`pulumi.StringOutput`). The component exports no stack output
of its own: the caller names those.

## Other provider components

None yet. Each provider's page lands here with its inputs, its children and
the alias each child carries.
