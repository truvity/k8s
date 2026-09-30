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

## Other provider components

None yet. Each provider's page lands here with its inputs, its children and
the alias each child carries.
