# k8s

**Kubernetes clusters as Pulumi components, behind one provider-neutral contract.**

Provisions the cluster and the things a cluster needs around it — the
network, the peering, a registry pull-through cache, the identity of the
cluster's controllers — as Pulumi `ComponentResource`s with documented child
names and names the caller supplies, plus the Helm charts that render the
cluster's own objects. EKS Auto Mode is the first provider; self-hosted
Talos is the second. Whichever provider made it, the consumer of a cluster sees the
same thing: a name, an endpoint, a CA, an OIDC issuer and a set of
capabilities.

| What | Where |
| --- | --- |
| Go module `github.com/truvity/k8s` | `go get github.com/truvity/k8s` |
| `pkg/cluster` — the provider-neutral contract: outputs and capabilities, with a validator | Go package, no cloud dependency |
| `pkg/cluster/clusterout` — the contract as a Pulumi output (`Contract` of every provider component) | Go package |
| `pkg/aws/pullthroughcache` — ECR pull-through cache rules and their credential secrets, `truvity:k8s/aws:PullThroughCache` | Pulumi component |
| `pkg/aws/vpcpeering` — a cross-account, cross-region VPC peering with its routes, DNS-resolution options and private-zone associations, `truvity:k8s/aws:VpcPeering` | Pulumi component |
| `pkg/aws/vpc` — a VPC with its subnets, route tables, gateways, NAT, IPv6, hardened defaults, gateway endpoints and flow logs, `truvity:k8s/aws:Vpc` | Pulumi component |
| `pkg/aws/podidentity` — an EKS Pod Identity role with its trust policy, permissions and one association per service account, `truvity:k8s/aws:PodIdentity` | Pulumi component |
| `pkg/aws/ekscluster` — an EKS Auto Mode cluster with its secrets key, cluster and node roles and control-plane log group, `truvity:k8s/aws:EksCluster` | Pulumi component |
| `pkg/aws/eksoidc` — the external OIDC issuer association of an EKS cluster, `truvity:k8s/aws:EksOidc` | Pulumi component |
| `pkg/aws/ackfactory` — the AWS identity of the ACK controllers: the IAM and EKS controller roles (the EKS one optionally for the IAMRoleSelector feature gate), the service controllers' roles (adopting what is live, each with its own boundary), the per-project builder roles with opt-in tightenings and a builder per employee sandbox; plain functions, names are API | Go package |
| `pkg/aws/iamkit` — loose IAM policies, roles and attachments registered with one provider, one permissions boundary and one logical-name prefix; names the IAM policy language | Go package |
| `pkg/aws/vpclayout` — the address plan of a fleet of VPCs as arithmetic over declared facts (CIDRs per network, subnet and availability zone) and a hub-and-spoke peering plan | Go package, no cloud dependency |
| `pkg/aws/autolb` — finds the load balancer EKS Auto Mode provisioned for a Service, by its tags | Go package (an AWS SDK read, not a resource) |
| `pkg/nscatalog` — the namespace catalog: one row per cluster and namespace, one column group per concern, its YAML encoding and a read-only check against a live cluster | Go package |
| `pkg/talos/schematic` — a Talos Image Factory schematic, its ID computed offline, the pinned installer image reference | Go package |
| `pkg/talos/machineconfig` — a self-hosted Talos cluster's machine configs rendered offline from a declared cluster and its secrets, validated as Talos does; the talosconfig; the contract | Go package |
| `pkg/talos/cilium` — the upstream Cilium chart's values for a Talos cluster | Go package |
| `cmd/identity-check` — proves a pod was admitted with its EKS Pod Identity or IRSA identity, see [identity-check](#identity-check) | Command and image, `ghcr.io/truvity/k8s/identity-check` |
| `charts/cilium-config` — LoadBalancer IP pools, L2 announcements (default) and optional BGP for Cilium, as data | Helm chart, `oci://ghcr.io/truvity/charts/cilium-config` |
| `pkg/talos/oidc` — a ServiceAccount issuer's discovery document and JWKS, generated offline | Go package |
| `pkg/aws/oidcissuer` — publishes them to an S3-compatible bucket and registers the IAM OIDC provider (IAM roles for service accounts off EKS), `truvity:k8s/aws:OidcIssuer` | Pulumi component |
| `charts/cluster-baseline` — Pod Security Admission labels per namespace (default level, reasoned exemptions, warn-first rollout), a guard, and opt-in default-deny, quota and limit range | Helm chart, `oci://ghcr.io/truvity/charts/cluster-baseline` |
| `charts/cluster-foundation` — Namespaces with their Pod Security labels and deletion protection, ClusterRoleBindings, StorageClasses, PriorityClasses and the EKS Auto Mode NetworkPolicy switch, every name a value | Helm chart, `oci://ghcr.io/truvity/charts/cluster-foundation` |
| `charts/cluster-pki` — the cluster's internal CA as cert-manager objects: a self-signed or offline root, an optional intermediate, the issuer workloads use | Helm chart, `oci://ghcr.io/truvity/charts/cluster-pki` |
| `charts/cluster-network-policies` — a cluster's own NetworkPolicies as data: selector, policy types and rules per entry, each with a required reason, sync wave and optional deletion protection | Helm chart, `oci://ghcr.io/truvity/charts/cluster-network-policies` |
| `charts/eks-auto-node-pools` — Karpenter NodePools and NodeClasses for EKS Auto Mode, one disruption budget per pool, subnet tags and roles as values | Helm chart, `oci://ghcr.io/truvity/charts/eks-auto-node-pools` |
| `charts/guardrails-projects` — the guardrails of every project namespace from a list of project rows: Namespace with labels and Pod Security, baseline NetworkPolicy (shared rules filtered by the row's kind), quota and LimitRange by profile, Roles and RoleBindings, an ACK IAMRoleSelector for the row's ceiling role, and an opt-in ValidatingAdmissionPolicy that holds a project's PodIdentityAssociations to its own namespace and roles; data-bearing objects carry `Prune=false,Delete=false` | Helm chart, `oci://ghcr.io/truvity/charts/guardrails-projects` |
| `charts/local-volumes` — local PersistentVolumes on the nodes' user volumes, pinned to their node and claim, with no-provisioner StorageClasses | Helm chart, `oci://ghcr.io/truvity/charts/local-volumes` |
| `charts/talos-etcd-backup` — scheduled, age-encrypted etcd snapshots of a Talos cluster to an S3-compatible bucket | Helm chart, `oci://ghcr.io/truvity/charts/talos-etcd-backup` |
| `charts/tenancy` — DEPRECATED ([window](docs/decisions/0004-tenancy-deprecation.md)): per-tenant NetworkPolicy, quota, RBAC, NATS account and Pod Identity and the Namespace itself with its Pod Security labels, one profile per tier, every name a value | Helm chart, `oci://ghcr.io/truvity/charts/tenancy` |
| `charts/volume-snapshot-crds` — the CSI external-snapshotter CustomResourceDefinitions, mirrored verbatim at the upstream version pinned in `crdctl.yaml` | Helm chart, `oci://ghcr.io/truvity/charts/volume-snapshot-crds` |
| `charts/cilium-crds` — the Cilium CustomResourceDefinitions, mirrored verbatim at the upstream version pinned in `crdctl.yaml` | Helm chart, `oci://ghcr.io/truvity/charts/cilium-crds` |

The two CRD charts carry the UPSTREAM version they mirror, not this
repository's: a release publishes each only if that version is not in the
registry yet. Their CRDs are generated by `just crds` (which also sets the
chart version) and committed; `just crds-check` proves they still equal the
upstream.

Every package and chart above is in the current release; each release's
changes, and the version a package or chart arrived in, are in the
[CHANGELOG](CHANGELOG.md). The other charts are published at the release's version.

## Who it is for

A team that runs its own clusters from a Pulumi program in Go and wants the
cluster, its network and its supporting pieces as reusable components,
rather than as a copy of another team's infrastructure repository. It
assumes you already have a Pulumi backend, cloud credentials and a naming
scheme; it chooses none of them.

It deliberately does not pick your names, your address ranges, your
permission boundaries or your accounts, does not install workloads into the
cluster. The charts render cluster objects and never workloads:
`cluster-baseline` labels namespaces someone else owns, `cluster-foundation`
renders the foundation objects (namespaces, role bindings, storage classes),
`eks-auto-node-pools` the Karpenter pools and `tenancy` what each tenant namespace
needs around it; see
[the reference](docs/reference.md#chartscluster-baseline).

## The model

Three ideas.

A **component** is a Pulumi `ComponentResource` with a documented set of
child resources. The child names are part of the API: a caller that adopts
a component over existing infrastructure relies on them to keep its
resources' URNs, through aliases the component declares.

A **contract** is what every provider component reports, in
`pkg/cluster.Outputs`: name, endpoint, certificate authority, OIDC issuer
and the capabilities the cluster offers (node pools, storage, network
policy, workload identity, load balancing, API access). A consumer asks for
a capability; it does not branch on the provider.

A **caller-supplied name** is any name, tag, range or boundary a component
would otherwise have to choose. Components take them as inputs; none has a
default that belongs to one estate.

## Install and a worked example

```sh
go get github.com/truvity/k8s
```

Pin an exact version in your `go.mod` and read the [CHANGELOG](CHANGELOG.md)
bullets between the old pin and the new.

The contract is what a consumer of any provider writes against:

```go
out := cluster.Outputs{
	Provider:                cluster.ProviderEKS,
	Name:                    "example",
	Endpoint:                "https://api.example.test",
	CertificateAuthorityPEM: caPEM,
	OIDCIssuer:              "https://oidc.example.test/id/abc",
	Capabilities:            cluster.NewCapabilities(cluster.NodePools, cluster.WorkloadIdentity),
}
if err := out.Validate(); err != nil {
	return err // the provider reported something a consumer cannot use
}
if out.Capabilities.Has(cluster.WorkloadIdentity) {
	// wire pod identities against out.OIDCIssuer
}
```

## Consumers

A consumer adopts it cluster by cluster, each adoption proven by an empty
Pulumi preview (see
[docs/decisions/0002-urn-stability.md](docs/decisions/0002-urn-stability.md)).
No consumer is recorded; a repository that adopts it adds a line here.

## Neighbours

- [pulumi-pipeline](https://github.com/truvity/pulumi-pipeline) — runs many
  Pulumi stacks as one pipeline with refusal gates; the natural driver for
  programs that use these components.
- [policy](https://github.com/truvity/policy) — the contracts this repository
  is held to (`docs/contracts/component.md`).
- [ci-workflows](https://github.com/truvity/ci-workflows) — the shared CI and
  release workflows this repository calls.

## identity-check

`cmd/identity-check` is the gate a release puts in front of its workloads
so that none of them starts without its cloud identity. An EKS Pod Identity
association is eventually consistent ([CreatePodIdentityAssociation](https://docs.aws.amazon.com/eks/latest/APIReference/API_CreatePodIdentityAssociation.html)):
it can exist before the cluster's pod admission can see it, and credentials
are injected only when a pod is created. A pod admitted in that gap has no
credentials for its whole life, so the check is a Job with
`restartPolicy: Never`, whose retries are new pods.

It looks for the injected identity (`AWS_CONTAINER_CREDENTIALS_FULL_URI` and
`AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE` for Pod Identity, or `AWS_ROLE_ARN`
and `AWS_WEB_IDENTITY_TOKEN_FILE` for IRSA) and exits non-zero at once when
neither is there. Then it calls STS `GetCallerIdentity`, which needs no IAM
permission. It never reads the instance metadata service, so a node's role
cannot stand in for a missing pod identity. It logs JSON.

| Flag | Default | |
| --- | --- | --- |
| `-expect-role NAME` | none | fail unless the assumed role's name is `NAME` |
| `-expect-account ID` | none | fail unless the caller's AWS account is `ID` (12 digits, validated at startup; the STS `Account` field must also agree with the ARN's account) |
| `-timeout` | `30s` | bound on the STS call, retries included |

The image is `ghcr.io/truvity/k8s/identity-check:<version>` (the release
version without the `v`), static, distroless and nonroot, for linux/amd64 and
linux/arm64. Charts do not write the Job themselves: they include
truvity/policy's `service-lib.identityCheck` template.

## Documentation

- [docs/adoption.md](docs/adoption.md) — using a component, adopting one over
  existing infrastructure, and every breaking upgrade.
- [docs/reference.md](docs/reference.md) — the Go types and the capability set.
- [docs/talos.md](docs/talos.md) — provisioning, changing and upgrading a
  self-hosted Talos cluster: secrets, image schematics, first boot, talosctl
  upgrades, recovery.
- [docs/safety.md](docs/safety.md) — what is refused and what must never be
  done to a cluster's resources.
- [docs/doctrine.md](docs/doctrine.md) — why it is shaped this way.
- [docs/decisions/](docs/decisions/) — the decisions, one page each.
- [SECURITY.md](SECURITY.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## The rule that makes this repository public

Mechanism only. A cluster's name, its address ranges, its account, its
permission boundaries and every tag pattern are the caller's: an input, a
hook the caller supplies, or a file the caller commits. Nothing here
defaults to one estate's value, and the tests never reach a real cloud —
providers run against Pulumi mocks and cluster behaviour against kind.
[`hack/leak-canary.sh`](hack/leak-canary.sh) enforces the mechanical half
over tracked files and runs in the gate.

## Status

v0: the API may still move in a minor release, and every move is a
`Breaking:` bullet in the [CHANGELOG](CHANGELOG.md). The module holds the
contract, the EKS provider's components and the Talos packages, and the
charts; providers are added by extraction from a running estate, each with
its URN-stability proof.

## Development

```sh
devbox shell        # pins every tool
just check          # build, test, lint, leak-canary
just vuln           # reachable Go advisories (its own workflow in CI, not in check)
```

## Releasing

Releases are a `v*` tag, which the shared release workflow turns into the
Go module version and a GitHub Release. Automatic patch releases are not
armed; the first release and every minor and major are hand-cut tags.

## Licence

MIT. See [LICENSE](LICENSE).
