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

## `pkg/aws/vpc`

`truvity:k8s/aws:Vpc` deploys one VPC: the VPC, its subnets and route tables,
the gateways, NAT, IPv6, hardened defaults, gateway endpoints and flow logs.
The caller states the layout; the component adds what every such VPC needs.
It takes one AWS provider in `Args.Provider`, never from `pulumi.Providers`.

```go
v, err := vpc.New(ctx, "net", &vpc.Args{
	Provider:          provider,
	Region:            "us-east-1",
	CIDR:              "10.0.0.0/16",
	AvailabilityZones: []string{"us-east-1a", "us-east-1b"},
	Subnets: []vpc.Subnet{
		{Name: "public", Type: vpc.Public, IPv6Index: 0, CIDRs: map[string]string{
			"us-east-1a": "10.0.0.0/24", "us-east-1b": "10.0.1.0/24"}},
		{Name: "app", Type: vpc.Private, IPv6Index: 4, CIDRs: map[string]string{
			"us-east-1a": "10.0.4.0/24", "us-east-1b": "10.0.5.0/24"}},
	},
	Names: func(c vpc.Child) string { /* the names your stack already uses */ },
})
// v.VPCID, v.SubnetIDs["app"]["us-east-1a"], ...: export them under the names you choose.
```

### `Args`

| Field | Default | Meaning |
| --- | --- | --- |
| `Provider` | required | The AWS provider every child uses. |
| `Region` | required with endpoints | For the endpoints' service names, `com.amazonaws.<region>.<service>`. |
| `CIDR` | required | The VPC's IPv4 CIDR, `/16` to `/28`. |
| `DisableIPv6` | IPv6 on | On: an Amazon-provided `/56`, a `/64` per subnet and zone with addresses assigned on creation, `::/0` to the internet gateway in the public route table and to an egress-only gateway in each private one. |
| `AvailabilityZones` | required | The zones the VPC spans; every subnet has one CIDR in each, and each gets a private route table. |
| `Subnets` | required | `Name`, `Type` (`vpc.Public` or `vpc.Private`), `CIDRs` (zone to IPv4 CIDR, inside `CIDR`, no overlap), `IPv6Index` (first `/64` index in the `/56`; the subnet's zones, sorted, take it and the following ones), `Tags`. |
| `NAT` | `vpc.NATSingle` | `NATSingle`: one gateway in the first zone, every private route table routes to it. `NATPerZone`: a gateway per zone, each private route table routes to its own. `NATNone`: no gateway and no IPv4 default route. Only when there is a private subnet. |
| `NATSubnet` | first public subnet | The public subnet the NAT gateways sit in. |
| `GatewayEndpoints` | `s3` and `dynamodb` | Nil means both, a non-nil empty slice none. Attached to the public route table and every private one. |
| `KeepDefaultSecurityGroup` | false | Leave the default security group alone. Otherwise it is emptied. |
| `KeepDefaultNACL` | false | Leave the default network ACL alone. Otherwise the component owns it (below). |
| `DefaultNACLHTTPSIngress` | none | Up to six IPv4 CIDRs allowed to reach port 443, as rules 114 to 119. For peered VPCs. |
| `FlowLogs` | on | `Disable`; `LogGroupName` (`/vpc/flow-logs/<component>`), `RoleName` (`<component>-flow-logs`), `RetentionDays` (365), `TrafficType` (`ALL`), `AggregationSeconds` (60), `PermissionsBoundaryARN` (none). |
| `Tags` | none | On every child that takes tags. |
| `ChildTags` | none | `func(Child) map[string]string`, extra tags per child, such as `Name`. |
| `Names` | `DefaultName` | Naming hook, `func(Child) string`. |
| `LegacyTopLevel` | false | Adopt loose resources by alias. |
| `Protect` | true | `*bool`. |

A child's tags, in increasing precedence: `Name` (the child's `DefaultName`),
the component's structural tags (`LogicalSubnet`, `Type` and
`AvailabilityZone` on subnets; `Type` and, on private tables,
`AvailabilityZone` on route tables), `Tags`, `ChildTags` and, on a subnet,
`Subnet.Tags`.

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: a missing provider, a bad, non-IPv4
or wrongly sized CIDR, no zones, a repeated or empty zone, no subnets, a
subnet with no name, a repeated name or a bad type, a subnet that has no CIDR
in a listed zone or has one in an unlisted zone (the zone count must match),
subnet CIDRs outside the VPC or overlapping, IPv6 indexes that overlap or do
not fit the `/56`, a bad NAT mode, a NAT with no public subnet to sit in, a
`NATSubnet` that is unknown, private or set with `NATNone`, an unknown or
repeated endpoint service, a missing region while there are endpoints, more
than six HTTPS sources or a bad one, bad flow-log settings, and a naming
hook that returns an empty name or the same name twice for children of one
Pulumi type. (A subnet and its route table association are different types,
so a hook may give them one name, as an existing state often does.)

### Children

`Names` receives a `Child` (`Component`, `Kind`, `Subnet`, `AZ`, `Service`)
and returns the logical name; the defaults below use the component name
`<c>`. Names are API.

| Child | Type | Default name | Present when |
| --- | --- | --- | --- |
| VPC | `aws:ec2/vpc:Vpc` | `<c>-vpc` | always (protected) |
| default security group | `aws:ec2/defaultSecurityGroup:DefaultSecurityGroup` | `<c>-default-sg` | not `KeepDefaultSecurityGroup` |
| default network ACL | `aws:ec2/defaultNetworkAcl:DefaultNetworkAcl` | `<c>-default-nacl` | not `KeepDefaultNACL` |
| internet gateway | `aws:ec2/internetGateway:InternetGateway` | `<c>-igw` | a public subnet exists |
| public route table | `aws:ec2/routeTable:RouteTable` | `<c>-public-rt` | a public subnet exists |
| public routes | `aws:ec2/route:Route` | `<c>-public-route-ipv4`, `<c>-public-route-ipv6` | a public subnet exists (IPv6 route with IPv6) |
| egress-only gateway | `aws:ec2/egressOnlyInternetGateway:EgressOnlyInternetGateway` | `<c>-eoigw` | a private subnet exists and IPv6 is on |
| private route table | `aws:ec2/routeTable:RouteTable` | `<c>-private-rt-<az>` | per zone, when a private subnet exists |
| private IPv6 route | `aws:ec2/route:Route` | `<c>-private-route-ipv6-<az>` | per zone, with IPv6 |
| subnet | `aws:ec2/subnet:Subnet` | `<c>-subnet-<subnet>-<az>` | per subnet and zone (protected) |
| association | `aws:ec2/routeTableAssociation:RouteTableAssociation` | `<c>-subnet-<subnet>-<az>-assoc` | per subnet and zone |
| NAT address | `aws:ec2/eip:Eip` | `<c>-nat-eip-<az>` | one (first zone) with `NATSingle`, one per zone with `NATPerZone` |
| NAT gateway | `aws:ec2/natGateway:NatGateway` | `<c>-nat-<az>` | same |
| NAT route | `aws:ec2/route:Route` | `<c>-nat-route-<az>` | per zone's private route table |
| gateway endpoint | `aws:ec2/vpcEndpoint:VpcEndpoint` | `<c>-<service>-endpoint` | per entry of `GatewayEndpoints` |
| flow log group | `aws:cloudwatch/logGroup:LogGroup` | `<c>-flow-log-group` | not `FlowLogs.Disable` |
| flow log role | `aws:iam/role:Role` | `<c>-flow-log-role` | same |
| flow log policy | `aws:iam/rolePolicy:RolePolicy` | `<c>-flow-log-policy` | same; write access to the log group only |
| flow log | `aws:ec2/flowLog:FlowLog` | `<c>-flow-log` | same |

Every child uses `Args.Provider`.

### The default network ACL

With `KeepDefaultNACL` unset the component manages the default ACL with a
`DefaultNetworkAcl`, which **replaces the ACL's whole rule list on every
apply**. The component is therefore its sole owner: an `ec2.NetworkAclRule`
created elsewhere against that ACL is deleted by the next apply while its
own stack's state still claims it. Admit extra traffic with
`DefaultNACLHTTPSIngress`, or set `KeepDefaultNACL` and own the ACL yourself.

Inbound, in order: all traffic from the VPC (100, IPv6 101); ephemeral TCP
return traffic (110 to 113); HTTPS from `DefaultNACLHTTPSIngress` (114 to
119); DNS UDP return (120, 121); NTP return (130); ephemeral UDP return (140
to 143). Outbound: all traffic to the VPC (100, 101); HTTPS (110, 111); DNS
over UDP and TCP (120 to 123); NTP (130); ephemeral TCP (140 to 143) and UDP
(150 to 153). The ephemeral ranges skip port 3389, so no rule admits an admin
port from anywhere. Everything else is denied by the ACL's implicit last
rule. IPv6 rules appear only with IPv6.

### Aliases

With `LegacyTopLevel` every child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the name the
hook gives it, the same type, no parent. The AWS SDK declares no aliases of
its own on these types today (ADR 0003 item 6); a test fails when an upgrade
adds one the component does not mirror.

### Protection

The VPC and every subnet are protected unless `Protect` points at false:
replacing either destroys everything that lives in them, so a preview that
would delete or replace one fails instead. No other child is protected; they
are rebuilt from the program. Point `Protect` at false only for a VPC that is
meant to be torn down.

### Outputs

`VPCID`, `IPv6CIDR`, `InternetGatewayID`, `EgressOnlyGatewayID`,
`PublicRouteTableID`, `FlowLogID` (`pulumi.StringOutput`; an empty-string
output for what the args did not ask for), `CIDR` (the string given), and the
maps `PrivateRouteTableIDs` (zone), `SubnetIDs` (subnet, zone),
`NATGatewayIDs` and `NATEIPIDs` (the zone each sits in) and
`GatewayEndpointIDs` (service). The component exports no stack output of its
own: the caller names those.

## `pkg/aws/podidentity`

`truvity:k8s/aws:PodIdentity` deploys one EKS Pod Identity role: the IAM role
whose trust policy admits the EKS Pod Identity service for one namespace and
its service accounts, the permissions it carries, and one
`PodIdentityAssociation` per service account. A role and its associations are
one component because the trust policy is what binds the role to those service
accounts. It takes its AWS provider in `Args.Provider`, never from
`pulumi.Providers`.

```go
id, err := podidentity.New(ctx, "reports", &podidentity.Args{
	Provider:        provider,
	ClusterName:     cluster.Name,
	Namespace:       "reports",
	ServiceAccounts: []string{"reports-api", "reports-job"},
	RoleName:        "reports",
	AccountID:       accountID,
	ClusterARN:      clusterARN,
	InlinePolicy:    &podidentity.InlinePolicy{Name: "access", Document: doc},
	Names: func(c podidentity.Child) string { /* the names your stack already uses */ },
})
// id.RoleARN, id.RoleName: export them under the names you choose.
```

### `Args`

| Field | Meaning |
| --- | --- |
| `Provider` | The AWS provider of the cluster's account and region. Required. |
| `ClusterName` | The cluster the associations are in; pass the cluster's `Name` output to order them after it. Required. |
| `Region` | Set on each association when not empty. Empty leaves the provider's region in force. |
| `Namespace`, `ServiceAccounts` | Which pods may assume the role: one association per service account. Required, no repeats. |
| `RoleName` | The IAM role's name. Required. |
| `PermissionsBoundary` | A permissions boundary ARN. Empty sets none. |
| `AccountID`, `ClusterARN` | Pin the default trust policy to the cluster (`aws:SourceAccount`, `aws:SourceArn`). Required unless `TrustPolicy` is set. |
| `TrustPolicy` | Replaces the default trust policy; used verbatim. For adopting a role whose trust document was rendered another way (IAM compares the JSON, the provider compares the string). |
| `InlinePolicy` | `Name`, `Document`: an inline role policy. |
| `ManagedPolicy` | `Name`, `Description`, `Document`: a customer-managed policy the component creates and attaches. |
| `IgnoreAssociationTags` | Ignore changes to the associations' `tags` and `tagsAll`. |
| `Imports` | IDs of existing `Role`, `Policy`, `Attachment` and per-service-account `Associations` to adopt. Empty creates the child. |
| `Names` | Naming hook, `func(Child) string`. Nil: `DefaultName`. |
| `LegacyTopLevel` | Adopt loose resources by alias. |
| `Protect` | `bool`; false (the default) protects nothing. |

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: a missing provider or cluster, an
empty role name or namespace, no service account, an empty or repeated service
account, no way to render the trust policy (neither `TrustPolicy` nor
`AccountID` and `ClusterARN`), an inline or managed policy with no name or
document, an import for a service account that is not listed, and a naming
hook that returns an empty or repeated name.

The default trust policy is always scoped: the EKS Pod Identity service, the
actions `sts:AssumeRole` and `sts:TagSession`, `aws:SourceAccount` and
`aws:SourceArn` for the cluster, and `aws:RequestTag/kubernetes-namespace` and
`aws:RequestTag/kubernetes-service-account` for the namespace and service
accounts (a string for one, a list in the order given for several). A trust
that is not scoped that way can only come through `TrustPolicy`.

### Children

`Names` receives a `Child` (`Component`, `Kind`, `Key`) and returns the
logical name; the defaults below use the component name `<c>`. Names are API.

| Child | Type | Default name | Present when |
| --- | --- | --- | --- |
| policy | `aws:iam/policy:Policy` | `<c>-policy` | `ManagedPolicy` is set |
| role | `aws:iam/role:Role` | `<c>-role` | always |
| inline policy | `aws:iam/rolePolicy:RolePolicy` | `<c>-role-policy` | `InlinePolicy` is set |
| attachment | `aws:iam/rolePolicyAttachment:RolePolicyAttachment` | `<c>-attachment` | `ManagedPolicy` is set |
| association | `aws:eks/podIdentityAssociation:PodIdentityAssociation` | `<c>-pia-<sa>` | one per service account (`Key` is the service account) |

### Adopting existing resources

`Imports` makes a child adopt what exists. The IDs must be known when the
program declares the resource, so the caller looks them up first. An
association's import ID (`<cluster>,<association id>`) differs from the ID the
provider keeps in state (the bare association id): leave an import option on
an association that is already in state and the next preview plans a
replacement, whose delete succeeds and whose create may not. Set
`Imports.Associations` only while adopting.

### Aliases

With `LegacyTopLevel` every child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the name the
hook gives it, the same type, no parent. The AWS SDK declares no aliases of
its own on these types today (ADR 0003 item 6); a test fails when an upgrade
adds one the component does not mirror.

### Protection

Nothing is protected unless `Protect` is true, which protects the role, the
managed policy and the associations (not the inline policy or the attachment,
which are replaced with the role).

### Outputs

`RoleARN`, `RoleName` (`pulumi.StringOutput`). The component exports no stack
output of its own: the caller names those.

## `pkg/aws/ekscluster`

`truvity:k8s/aws:EksCluster` deploys one EKS Auto Mode cluster: the cluster,
the KMS key its secrets are encrypted with, the cluster's IAM role and the
Auto Mode node role with their policies, and the CloudWatch log group of the
control plane. The shape is fixed: API authentication mode, no bootstrap
creator-admin, no self-managed add-ons, a rotated customer-managed key,
control-plane logs on, EKS-managed compute, load balancing and block storage.
Access entries, security group rules, DNS records and add-ons are the
caller's, attached to `Cluster`. It takes its AWS provider in `Args.Provider`,
never from `pulumi.Providers`.

```go
c, err := ekscluster.New(ctx, "main", &ekscluster.Args{
	Provider:        provider,
	Name:            "c1",
	Version:         "1.31",
	SubnetIDs:       subnetIDs,
	ServiceCIDR:     "172.20.0.0/16",
	ClusterPolicies: clusterPolicyARNs,
	NodePolicies:    nodePolicyARNs,
	NodeInlinePolicies: []ekscluster.InlinePolicy{{Name: "pull", Document: doc}},
	Names: func(ch ekscluster.Child) string { /* the names your stack already uses */ },
})
// c.Cluster, c.KeyARN, c.NodeRoleARN: use and export them under the names you choose.
```

### `Args`

| Field | Meaning |
| --- | --- |
| `Provider` | The AWS provider of the cluster's account and region. Required. |
| `Name`, `Version` | The cluster's name and Kubernetes version. Required. |
| `SubnetIDs` | The subnets of the control plane and the nodes. Required, no repeats. |
| `ServiceCIDR` | The service IPv4 range, with no host bits. Create-time immutable: another value replaces the cluster. Required. |
| `PublicEndpoint` | Opens the API endpoint to the internet. Default false: private-only. |
| `NodePools` | EKS-managed node pools. Default `DefaultNodePools()`: `general-purpose`, `system`. |
| `LogTypes` | Control-plane log types. Default `DefaultLogTypes()`: `api`, `audit`, `authenticator`, `controllerManager`, `scheduler`. |
| `PermissionsBoundary` | A permissions boundary ARN for both roles. Empty sets none. |
| `ClusterRoleName`, `NodeRoleName` | The role names. Default `<Name>-cluster` and `<Name>-node`; they must differ. |
| `ClusterTrustPolicy`, `NodeTrustPolicy` | Replace the default trust documents; used verbatim. For adopting a role whose document was rendered another way (IAM compares the JSON, the provider compares the string). |
| `ClusterPolicies` | ARNs of the managed policies on the cluster role. Required: the caller passes ARNs, the component holds none. |
| `NodePolicies` | ARNs of the managed policies on the node role. |
| `NodeInlinePolicies` | `Name`, `Document`: inline policies of the node role. Names are unique. |
| `KeyDescription`, `KeyAlias` | Default `EKS <Name> cluster secrets encryption` and `alias/eks-<Name>-secrets`. |
| `KeyRotationDays` | 90 to 2560. Default 365. Rotation is always on. |
| `LogGroupName`, `LogRetentionDays` | Default `/aws/eks/<Name>/cluster` (the group EKS writes to) and 90 days. |
| `Tags` | Set on the key, the roles, the log group and the cluster. |
| `Names` | Naming hook, `func(Child) string`. Nil: `DefaultName`. |
| `LegacyTopLevel` | Adopt loose resources by alias. |
| `Protect` | `*bool`; nil (the default) protects. |

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: a missing provider, name, version,
subnet or service CIDR, a service CIDR that is not IPv4 or has host bits set,
an empty or repeated subnet or policy ARN, no cluster policy, an inline policy
with no name, no document or a repeated name, one name for both roles, an
unknown log type, an empty node pool, a key rotation period outside 90 to
2560 days, a negative retention, and a naming hook that returns an empty or
repeated name.

The cluster is created after both roles, every policy attachment and the log
group.

### Children

`Names` receives a `Child` (`Component`, `Kind`, `Key`) and returns the
logical name; the defaults below use the component name `<c>`. `<key>` is the
last path segment of a policy ARN, `<name>` an inline policy's name. Names are
API.

| Child | Type | Default name | Present when |
| --- | --- | --- | --- |
| key | `aws:kms/key:Key` | `<c>-key` | always |
| key alias | `aws:kms/alias:Alias` | `<c>-key-alias` | always |
| cluster role | `aws:iam/role:Role` | `<c>-cluster-role` | always |
| cluster policy | `aws:iam/rolePolicyAttachment:RolePolicyAttachment` | `<c>-cluster-role-<key>` | one per `ClusterPolicies` entry (`Key` is `<key>`) |
| node role | `aws:iam/role:Role` | `<c>-node-role` | always |
| node policy | `aws:iam/rolePolicyAttachment:RolePolicyAttachment` | `<c>-node-role-<key>` | one per `NodePolicies` entry |
| node inline policy | `aws:iam/rolePolicy:RolePolicy` | `<c>-node-role-policy-<name>` | one per `NodeInlinePolicies` entry |
| log group | `aws:cloudwatch/logGroup:LogGroup` | `<c>-logs` | always |
| cluster | `aws:eks/cluster:Cluster` | `<c>-cluster` | always |

### Aliases

With `LegacyTopLevel` every child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the name the
hook gives it, the same type, no parent. The AWS SDK declares no aliases of
its own on these types today (ADR 0003 item 6); a test fails when an upgrade
adds one the component does not mirror.

### Protection

The cluster, the KMS key and both roles are protected unless `Protect` points
at false: replacing the cluster is an outage, deleting the key loses what
decrypts the cluster's secrets, and the roles are what the cluster runs as. A
preview that would delete or replace one fails instead. No other child is
protected; they are rebuilt from the program. Point `Protect` at false only
for a cluster that is meant to be torn down.

### Outputs

`Cluster` (`*eks.Cluster`), `KeyARN`, `ClusterRoleARN`, `ClusterRoleName`,
`NodeRoleARN`, `NodeRoleName` (`pulumi.StringOutput`). The component exports no
stack output of its own: the caller names those.

## `pkg/aws/eksoidc`

`truvity:k8s/aws:EksOidc` points one EKS cluster's API server at an external
OIDC issuer: one `eks.IdentityProviderConfig`. EKS validates the bearer token
and takes the username and the groups from its claims. It takes its AWS
provider in `Args.Provider`, never from `pulumi.Providers`. The association is a
full cluster update (10 to 15 minutes), so callers usually run it as a stack of
its own, last.

```go
o, err := eksoidc.New(ctx, "main", &eksoidc.Args{
	Provider:    provider,
	ClusterName: cluster.Name,
	ConfigName:  "issuer",
	IssuerURL:   "https://issuer.example.test",
	ClientID:    "k8s:c1",
	Names:       func(eksoidc.Child) string { /* the name your stack already uses */ },
})
```

### `Args`

| Field | Meaning |
| --- | --- |
| `Provider` | The AWS provider of the cluster's account and region. Required. |
| `ClusterName` | The cluster, a `pulumi.StringInput`. Pass the cluster's `Name` output to order the association after it. Required. |
| `ConfigName` | The association's name inside EKS. Required. |
| `IssuerURL` | The issuer, an `https://` URL. Required. |
| `ClientID` | The audience of every token the cluster accepts. Required. |
| `UsernameClaim` | Default `sub`. |
| `UsernamePrefix` | Default `-`, EKS's value for no prefix. Left unset EKS prefixes the issuer, which turns every binding on a bare name into another name. |
| `GroupsClaim` | Default `groups`. |
| `Names` | Naming hook, `func(Child) string`. Nil: `DefaultName`. |
| `LegacyTopLevel` | Adopt a loose resource by alias. |
| `Protect` | `*bool`; nil (the default) protects. |

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: a missing provider or cluster name, an
empty client id, association name or issuer URL, an issuer URL that is not
https, and a naming hook that returns an empty name.

### Children

| Child | Type | Default name | Present when |
| --- | --- | --- | --- |
| provider config | `aws:eks/identityProviderConfig:IdentityProviderConfig` | `<c>-oidc` | always |

Names are API: renaming the child replaces the association. EKS admits one
external OIDC association per cluster, so a replace disassociates first and
sign-in through the issuer is down for both legs; the child is therefore always
registered with delete-before-replace.

### Aliases

With `LegacyTopLevel` the child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the name the
hook gives it, the same type, no parent. The AWS SDK declares no aliases of its
own on this type today (ADR 0003 item 6); a test fails when an upgrade adds one
the component does not mirror.

### Protection

The association is protected unless `Protect` points at false: a preview that
would delete or replace it fails instead. Point `Protect` at false only for a
cluster that is meant to be torn down.

### Outputs

`ProviderConfig` (`*eks.IdentityProviderConfig`). The component exports no stack
output of its own: the caller names those.

## Other provider components

None yet. Each provider's page lands here with its inputs, its children and
the alias each child carries.

## `charts/cluster-baseline`

Pod Security Admission labels, per namespace, as data. Install a pinned
version from `oci://ghcr.io/truvity/charts/cluster-baseline`, for example
`--version 0.8.0`; see [adoption](adoption.md#cluster-baseline-warn-first).

The chart renders one `Namespace` per entry of `podSecurity.namespaces` with
only `metadata.name`, labels and annotations. Apply it server-side
(`ServerSideApply=true` in Argo CD, `kubectl apply --server-side
--field-manager=cluster-baseline`, or Helm 4's `--server-side`): the object
then owns the PSA labels and nothing else, and the namespace's real owner
keeps the rest. A name that does not exist yet is created, empty.

| Value | Default | Meaning |
| --- | --- | --- |
| `podSecurity.level` | `restricted` | The level every mode that is on uses. `privileged`, `baseline` or `restricted`. |
| `podSecurity.version` | `""` | Pin of the Pod Security Standards version for every mode: `latest` or `v1.<minor>`. Empty renders no version label, which the API server reads as latest. |
| `podSecurity.modes.<kind>.enabled` | `warn`: true, `audit`: true, `enforce`: false | One switch per label kind. Off renders no label of that kind. |
| `podSecurity.modes.<kind>.level` / `.version` | `""` | Replace the two values above for this one kind; empty inherits. |
| `podSecurity.namespaces.<name>` | `{}` | The namespaces to label. An empty entry takes the defaults. The name is a DNS label. |
| `podSecurity.namespaces.<name>.level` | unset | Replaces the level for every mode that is on in this namespace. Requires `reason`. |
| `podSecurity.namespaces.<name>.version` | unset | Replaces the version pin for this namespace. |
| `podSecurity.namespaces.<name>.modes.<kind>.level` / `.version` | unset | Overrides one kind and renders its label even when the kind is off cluster-wide. This is the enforce canary. A level that differs from the default requires `reason`. |
| `podSecurity.namespaces.<name>.reason` | unset | Why this namespace departs from the defaults. Required by the render for any override of the level or any kind that is off; rendered into `reasonAnnotation`. |
| `protect` | `true` | Annotate each Namespace so that Helm keeps it and Argo CD neither prunes nor deletes it. |
| `reasonAnnotation` | `cluster-baseline/psa-reason` | The annotation key that carries the reason. |

Resolution, per namespace and per kind: the namespace's `modes.<kind>.level`
if set, else (when the kind is on) the namespace `level`, else the kind's
`level`, else `podSecurity.level`. The version resolves the same way through
`modes.<kind>.version`, namespace `version`, kind `version` and
`podSecurity.version`. No level resolves to no label.


### Not in this chart yet

Proposed, deliberately left out of the first version so that the one
mechanism can be reviewed alone:

- A default-deny `NetworkPolicy` per listed namespace, with an allow list for
  DNS. Needs the `network-policy` capability, and breaks every workload that
  was relying on open traffic, so it would ship warn-first too (a no-op
  policy first, then enforcing).
- `ResourceQuota` and `LimitRange` defaults per namespace, inputs only.
- A guard (`ValidatingAdmissionPolicy`, GA from Kubernetes 1.30) that warns
  when a namespace is created without PSA labels, closing the gap that only
  listed namespaces are covered.
