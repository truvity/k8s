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
| `cmd/netpol-stuck-pod-repair` — **TEMPORARY workaround** for the EKS network policy agent race [aws/aws-network-policy-agent#676](https://github.com/aws/aws-network-policy-agent/issues/676) (fix [#705](https://github.com/aws/aws-network-policy-agent/pull/705), unreleased): deletes Deployment pods stranded with an empty egress map, within strict limits, report-only by default; see [netpol-stuck-pod-repair](#netpol-stuck-pod-repair) | Command and image, `ghcr.io/truvity/k8s/netpol-stuck-pod-repair` |
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
| `charts/netpol-stuck-pod-repair` — **TEMPORARY**: the one Deployment, RBAC, PodMonitor and optional NetworkPolicy of `cmd/netpol-stuck-pod-repair`; uninstall when the node image ships aws-network-policy-agent with #705 | Helm chart, `oci://ghcr.io/truvity/charts/netpol-stuck-pod-repair` |
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
cluster. The charts render cluster objects and never workloads (one
exception, temporary: `netpol-stuck-pod-repair`, a cluster-wide repair
controller that goes when the bug it works around is fixed upstream):
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

## netpol-stuck-pod-repair

**A TEMPORARY WORKAROUND.** The EKS network policy agent can lose a race
between a pod's CNI ADD and the reconcile of its PolicyEndpoint
([aws/aws-network-policy-agent#676](https://github.com/aws/aws-network-policy-agent/issues/676);
the fix, [#705](https://github.com/aws/aws-network-policy-agent/pull/705), is
not merged or released). The pod is Running and its PolicyEndpoints are
correct, but the eBPF map its program reads is empty, so every egress
connection times out (`dial tcp ...: i/o timeout`). It hits pods started in a
burst on a fresh node and same-node replacements. Restarting the containers
does not help; deleting the pod does (new pod, new IP, fresh maps). On EKS
Auto Mode the agent is part of the node image, so there is nothing to upgrade.

**Removal condition.** Uninstall it when the EKS Auto Mode node image ships
an aws-network-policy-agent release that contains #705. Verify with a node
drift roll followed by two weeks in which
`netpol_repair_actions_total{mode="act"}` does not move. Then remove the
command, the chart, and this section.

**What it does.** One replica per cluster. Every `-interval` (1m) it lists
the namespaces with at least one NetworkPolicy and their pods. A pod is a
candidate when ALL of these hold (the `netpol_repair_candidates` gauge counts only pods that pass all of them):

1. its namespace has a NetworkPolicy, is in `-namespaces` (empty: all) and
   not in `-exclude-namespaces` (the chart excludes `kube-system`);
2. its controller is a ReplicaSet (with or without a Deployment, which is
   then the "owner"); never a StatefulSet, Job, DaemonSet or bare pod, never
   a pod labelled `cnpg.io/cluster`, never a terminating pod, never itself;
3. it is Running and EITHER its containers restarted `-restart-threshold`
   (3) times within `-restart-window` (15m) (counted from samples taken each
   tick; for a pod younger than the window, every restart since it started)
   OR it has been NotReady for `-not-ready-after` (10m), measured from its
   Ready condition's last transition;
4. EITHER its owner has at least one other Ready pod, OR it is its owner's
   only pod and has been stuck for `-single-replica-stuck-after` (10m). An
   owner whose pods are all NotReady is left alone (nothing proves the owner
   itself works), and so is the owner's last Ready pod;
5. it is the race, not an ordinary crash loop. BOTH must hold:
   a `PolicyEndpoint` (`networking.k8s.aws`) in its namespace lists the pod
   (network policy is actually enforced on it), AND a `-timeout-patterns`
   hit (`i/o timeout`, `connect: connection timed out`) is in a container's
   last termination message or the tail of its log (the previous
   container's, after a restart). This is the bug's signature: every egress
   connection times out. Not switchable. A triggered pod that fails either
   test is logged once as `skipped: not the race` (`skip` is
   `no-policy-endpoint` or `no-timeout-evidence`), is not a candidate and is
   never deleted. An application that fails its own probe, with no timeout
   in its output, is left alone; so is one that logs no timeout at all, which
   is the price of not deleting ordinary crash-loopers;
6. optional, off by default: its node is younger than `-max-node-age` (the
   race also hits same-node replacements on old nodes, hence off).

Then the limits, in order: no second repair of an owner within
`-owner-cooldown` (30m); at most one decision per tick and none within
`-min-delete-interval` (1m) of the last, cluster-wide; and the self-stop:
once `-max-repairs` (5) deletions fall within `-halt-window` (1h) and
another candidate turns up, it stops deleting for good, keeps reporting, and
sets `netpol_repair_halted` to 1. Only a restart of its pod clears that: a
burst that size is not the race, so a person looks first. All of this state
is in memory.

Per decision it records evidence: a Warning Event on the owner
(`NetpolStuckPodRepaired`, or `NetpolStuckPodCandidate` in report mode) and
one JSON log line with the pod, node, node age, restarts, stuck time, the
other Ready replicas, each container's last termination (reason, exit code,
message), the PolicyEndpoints that list the pod and the timeout evidence.
With `-node-diagnostic` (off by default) it also creates an EKS
[`NodeDiagnostic`](https://docs.aws.amazon.com/eks/latest/userguide/auto-get-logs.html)
named after the node (`-node-diagnostic-destination`: `node` keeps the
bundle on the node; a presigned PUT URL uploads it, and is readable by
anyone who can read the object), so the agent's logs survive for the
upstream issue. An existing one is left alone; none is deleted. Then, in
act mode, it deletes the pod (UID precondition).

**Modes.** `-mode=report` (the default, and the chart's) deletes nothing: it
logs `WOULD repair`, counts `mode="report"` and records the Event. Read a
cluster's reports before switching it to `-mode=act`.

**Metrics** (`:8080/metrics`; the chart's PodMonitor honours labels, since
`namespace` is the target pod's): `netpol_repair_actions_total{namespace,
owner_kind, owner, reason, mode}`, `netpol_repair_candidates{namespace,
owner_kind, owner, reason}` (at the last examination, before the limits),
`netpol_repair_halted` (0/1) and `netpol_repair_last_run_timestamp_seconds`.
`/healthz` is the process; `/readyz` fails when no examination succeeded in
three intervals. The alert rules are in truvity/observability
(`charts/platform-alerts`, group `netpolRepair`).

**RBAC** (one ClusterRole): pods `get, list, delete`; pods/log `get`; nodes
`get`; events `create`; networkpolicies `list`; replicasets `list`;
`networking.k8s.aws` policyendpoints `list`; and, only with
`nodeDiagnostic.enabled`, `eks.amazonaws.com` nodediagnostics `create`.

The image is `ghcr.io/truvity/k8s/netpol-stuck-pod-repair:<version>` (the
release version without the `v`), static, distroless and nonroot, for
linux/amd64 and linux/arm64; the chart's `image.tag` defaults to its own
appVersion, the same release.

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
Go module version and a GitHub Release. Minors and majors are hand-cut
tags. Patch releases for dependency bumps are cut automatically where the
`AUTO_RELEASE` variable is `true` for the repository: weekly, or at once for
a merged pull request labelled `security`. A version with no CHANGELOG
heading is such a patch.

## Licence

MIT. See [LICENSE](LICENSE).
