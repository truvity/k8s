# Self-hosted Talos clusters

How to provision, run and upgrade a Talos Linux cluster with this
repository's Talos provider. The pieces are mechanism; the cluster's names,
addresses, disks and secrets are yours. Every example uses `example.com` and
`10.0.0.0/24`.

| Piece | What it is |
| --- | --- |
| `pkg/talos/schematic` | The Image Factory schematic and its ID, computed offline; the installer reference. |
| `pkg/talos/machineconfig` | Every node's machine config, rendered offline and validated as Talos does; the talosconfig; the contract. |
| `charts/cilium-config` | The cluster network after Cilium is installed: LB-IPAM pools, L2 announcements, optional BGP. |
| `pkg/talos/oidc` | The ServiceAccount issuer's discovery document and JWKS, generated offline, for workload identity. |
| `charts/local-volumes` | Local PersistentVolumes on Talos user volumes, for databases. |
| `charts/talos-etcd-backup` | Scheduled, encrypted etcd snapshots to an S3-compatible bucket. |
| `charts/cluster-pki` | The cluster's internal CA as cert-manager issuers. |

## The model

A cluster is a **declaration** (a `machineconfig.Cluster`, committed) plus a
**secrets bundle** (generated once, stored encrypted). Everything else is
derived:

```
declaration + bundle ──Render──▶ node configs + talosconfig + contract
schematic.yaml ──ID──▶ pinned installer image
bundle ──oidc──▶ openid-configuration + jwks ──▶ public bucket
```

Rendering is offline and deterministic, so the same inputs give the same
configs on any machine, and a review reads what changes: the per-node
`RenderedPatch` holds no secret. Rendered configs DO hold the cluster's
secrets: never commit them; render them where they are applied.

## Secrets

```go
bundle, err := machineconfig.NewSecrets("v1.14.2")
data, err := machineconfig.MarshalSecrets(bundle) // talosctl's secrets.yaml format
```

Generate the bundle once per cluster and keep it like a root key, encrypted
at rest (sops with age, a secret store). It holds the Talos API, Kubernetes,
aggregator and etcd CAs, the ServiceAccount signing key, the bootstrap
token, the etcd encryption secret and the cluster identity. Losing it means
the cluster cannot be re-rendered; leaking it means anyone can join a node
or mint an admin certificate.

The ServiceAccount key is RSA on purpose: AWS does not accept ECDSA-signed
tokens for IAM roles for service accounts.

## Image schematics

The installer image a node installs and upgrades to is built by the Talos
Image Factory from a **schematic**: board overlay, kernel arguments, system
extensions. Commit the schematic and pin its ID in the declaration:

```yaml
# schematic.yaml
overlay:
  image: siderolabs/sbc-example
  name: example-board
customization:
  extraKernelArgs:
    - console=ttyS0,115200
  systemExtensions:
    officialExtensions:
      - siderolabs/iscsi-tools      # Longhorn
      - siderolabs/util-linux-tools # Longhorn
```

```go
s, _ := schematic.Parse(schematicYAML)
id, _ := s.ID() // equals what the factory returns for the same schematic
```

Keep a test in your repository that re-derives the ID from the committed
schematic and compares it with the pinned one: editing the schematic without
the ID is how a cluster silently upgrades to the old image. Register the
schematic with the factory once (`curl -X POST --data-binary @schematic.yaml
https://factory.talos.dev/schematics`) so the factory can build it; the ID it
returns is the one computed here.

Things the schematic must carry, not the machine config:

- **Kernel arguments on UKI boots.** A node that boots a unified kernel image
  (SecureBoot, many boards) ignores the machine config's install kernel
  arguments; put them in `extraKernelArgs`.
- **Every extension a machine config refers to.** An
  `ExtensionServiceConfig` for an extension the image lacks makes the
  service fail at every boot; on some versions the node reboot-loops.

A board the factory cannot build gets its own installer from your pipeline:
`Node.Installer = &schematic.Installer{Image: "registry.example.com/boards/installer"}`.

## Declaring the cluster

See [reference.md](reference.md#pkgtalosmachineconfig) for every field. The
choices that matter most:

- **Endpoint.** Prefer the control plane VIP's IP to a DNS name: a name
  served by something running in the cluster deadlocks a cold start. Add the
  name to `ControlPlane.CertSANs`.
- **Odd number of control plane nodes**, in different failure domains.
  `Render` warns about an even number.
- **`Network.NodeSubnets`** whenever a node has a second address (a VPN, a
  management network). Without it the kubelet may register, and etcd
  advertise, the address the other nodes cannot reach.
- **CNI none** (the default) and no kube-proxy: Cilium takes both, reaching
  the API through KubePrism on `localhost:7445`. The nodes stay NotReady
  until Cilium runs; that is expected.
- **Taints.** Talos applies `Node.Taints` when the node registers. On a node
  that is already registered, the NodeRestriction admission plugin keeps the
  kubelet from changing them: set them once with `kubectl taint` too. A
  control plane that must also run pods with local volumes is better served
  by `PreferNoSchedule` than by removing the taint.
- **Patches** merge the way Talos merges: maps and structs merge, most lists
  append. To replace a list entry, delete it first with `$patch: delete`.

## First provisioning

```sh
# 1. Render (your program calls machineconfig.Render) into a private dir:
umask 077; mkdir -p rendered
# rendered/<hostname>.yaml, rendered/talosconfig

# 2. Boot every node from the factory ISO or disk image of the SAME
#    schematic and Talos version, then apply its config (maintenance mode):
talosctl apply-config --insecure -n 10.0.0.11 --file rendered/cp-1.yaml
# ... every node

# 3. Bootstrap etcd on ONE control plane node, once:
export TALOSCONFIG=$PWD/rendered/talosconfig
talosctl bootstrap -n 10.0.0.11 -e 10.0.0.11
talosctl health -n 10.0.0.11 -e 10.0.0.11 --wait-timeout 15m   # passes once a CNI runs

# 4. Kubeconfig, then the CNI (see charts/cilium-config):
talosctl kubeconfig -n 10.0.0.11 -e 10.0.0.11 rendered/kubeconfig
```

Use a `talosctl` of the same version as the nodes; pin it beside the
declaration. A distribution's package often lags.

## Changing the configuration

Re-render, then apply per node. Look before you leap:

```sh
talosctl apply-config -n 10.0.0.21 --file rendered/db-1.yaml --dry-run   # prints the diff
talosctl apply-config -n 10.0.0.21 --file rendered/db-1.yaml --mode auto
```

`--mode auto` reboots only when a change needs it. `--mode staged` applies at
the next reboot.

## Upgrading Talos

Bump `TalosVersion` (and the pinned `talosctl`), re-render, then upgrade
**one node at a time**, workers first, control plane last:

```sh
image=$(go run ./your/cmd installer-ref)   # Installer.Reference(TalosVersion)
talosctl upgrade -n 10.0.0.21 --image "$image"
```

After each node, verify instead of trusting the command's exit status:

```sh
talosctl version -n 10.0.0.21          # the new tag on the server side
talosctl get extensions -n 10.0.0.21   # the schematic's extensions are there
talosctl -n 10.0.0.11 etcd status       # control plane: every member healthy before the next
```

A node that cannot drain (a PodDisruptionBudget held by a single-replica
workload, a pod on a local volume) blocks the upgrade; move or stop that
workload first, or upgrade with `--drain=false` deliberately and accept the
interruption. Some versions exit 0 after staging the image without
rebooting the node: the version check above is what tells.

A minor upgrade (1.14 to 1.15) needs a release of this repository that
renders the new minor; `Render` refuses a minor it does not know.

## Upgrading Kubernetes

Bump `KubernetesVersion` (Talos' compatibility table must allow it; `Render`
checks), re-render and apply the configs, then let Talos roll the control
plane and the kubelets:

```sh
talosctl upgrade-k8s -n 10.0.0.11 --to 1.36.5 --dry-run
talosctl upgrade-k8s -n 10.0.0.11 --to 1.36.5
```

## Cluster PKI

Two layers, with different owners:

- **The cluster's own CAs** are in the secrets bundle: the Talos API CA, the
  Kubernetes CA (the contract's `CertificateAuthorityPEM`), the aggregator
  and etcd CAs, and the ServiceAccount key. Talos issues every component
  certificate from them and renews those itself. The CAs are valid for ten
  years; rotate one with `talosctl rotate-ca` (dry run first, then
  `--dry-run=false`, Talos API and Kubernetes separately), then re-export the
  bundle from the new machine configs so a re-render keeps the new CAs.
  Rotating the ServiceAccount key is a workload-identity rotation as well:
  publish the new key beside the old one first (`pkg/talos/oidc`).
- **The workloads' CA** is cert-manager's: `charts/cluster-pki` gives every
  in-cluster service one ClusterIssuer (`internal-ca` by default), backed by
  a self-signed root or by a root minted offline, optionally through an
  intermediate so the root key can leave the cluster. Distribute the root
  to clients (trust-manager's Bundle, or the nodes' trusted roots through a
  machine config patch for registry mirrors) and plan its rotation the same
  way: a new root trusted beside the old one before anything is re-issued.

## Workload identity (IAM roles for service accounts)

A pod gets AWS credentials by presenting a ServiceAccount token that AWS STS
verifies against the cluster's issuer. Four pieces, all offline until the
last:

1. **The issuer.** Set `Cluster.ServiceAccountIssuer` to an https URL you
   control, for example `https://oidc.example.com/clusters/example`. The API
   server signs tokens as that issuer and announces
   `<issuer>/openid/v1/jwks`; the endpoint stays an accepted issuer.
2. **The documents.** Generate them from the secrets bundle, no cluster
   needed:

   ```go
   keys, _ := oidc.PublicKeysFromPEM(bundle.Certs.K8sServiceAccount.Key)
   docs, _ := oidc.Generate(issuer, keys...)
   // docs.Discovery -> <issuer>/.well-known/openid-configuration
   // docs.JWKS      -> <issuer>/openid/v1/jwks
   ```

   They are what kube-apiserver serves itself (key IDs, algorithms, field
   order). A key rotation publishes the old and the new key together until
   no token signed with the old one is alive.
3. **Publication.** `pkg/aws/oidcissuer` writes both objects to an
   S3-compatible bucket (AWS S3, or a store reached through the AWS
   provider's S3 endpoint) and, with `IAMProvider`, registers the issuer as
   an IAM OpenID Connect provider (client ID `sts.amazonaws.com`). The bucket
   must serve them publicly at the issuer URL: a public-read policy on the
   prefix, or a CDN or custom domain in front. `oidcissuer.TrustPolicy`
   writes a role's trust policy for named ServiceAccounts
   (`<namespace>/<name>`, `*` allowed in the name).
4. **The webhook.** [amazon-eks-pod-identity-webhook][webhook] (the
   component that makes this "IRSA on Talos") mutates every pod whose
   ServiceAccount carries `eks.amazonaws.com/role-arn`: it projects a token
   with audience `sts.amazonaws.com` and sets `AWS_ROLE_ARN`,
   `AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_REGION` and `AWS_STS_REGIONAL_ENDPOINTS`.
   The AWS SDKs pick those up with no code change.

[webhook]: https://github.com/aws/amazon-eks-pod-identity-webhook

### Running the webhook

- **Image and serving certificate.** Run the upstream image (or a fork you
  maintain) with a serving certificate from cert-manager (the `cluster-pki`
  chart's issuer, or a self-signed one): the webhook's
  `MutatingWebhookConfiguration` needs the CA bundle injected
  (`cert-manager.io/inject-ca-from`).
- **Flags.** `--token-audience=sts.amazonaws.com`, `--aws-default-region`
  (required by recent versions; pods otherwise get no region),
  `--sts-regional-endpoint=true`, `--annotation-prefix=eks.amazonaws.com`.
  Keep `--in-cluster=false` and give it the certificate files.
- **Replicas.** Two, with a PodDisruptionBudget, spread over nodes.
- **Failure policy and ordering.** With `failurePolicy: Ignore` (usual: a
  webhook outage must not stop every pod), a pod created while the webhook
  is down starts WITHOUT credentials and stays so until recreated. Order
  every workload that needs AWS after the webhook (an Argo CD sync wave
  later than the webhook's), and check in CI that no annotated
  ServiceAccount sits in an earlier wave.
- **Bootstrap-critical pods** (an etcd backup, a secrets operator that
  unseals with a cloud KMS) should not depend on the webhook at all: give
  them the projected token and the environment by hand.

  ```yaml
  env:
    - {name: AWS_ROLE_ARN, value: "<role ARN>"}
    - {name: AWS_WEB_IDENTITY_TOKEN_FILE, value: /var/run/secrets/aws/token}
    - {name: AWS_REGION, value: "<region>"}
  volumeMounts:
    - {name: aws-token, mountPath: /var/run/secrets/aws, readOnly: true}
  volumes:
    - name: aws-token
      projected:
        sources:
          - serviceAccountToken: {audience: sts.amazonaws.com, path: token, expirationSeconds: 3600}
  ```

- **Key type.** The bundle's ServiceAccount key is RSA (RS256); keep it so
  for AWS.

### Checking it end to end

```sh
curl -fsS https://oidc.example.com/clusters/example/.well-known/openid-configuration
kubectl create token -n backup etcd-backup --audience sts.amazonaws.com > /tmp/t
aws sts assume-role-with-web-identity --role-arn "<role ARN>" \
  --role-session-name check --web-identity-token "file:///tmp/t"
```
## Storage

Two tiers, chosen per workload:

- **Local volumes for databases.** A workload that replicates its own data
  (a Postgres cluster, a NATS or Valkey cluster) runs one replica per node
  on that node's disk: no network storage in the write path, and the
  application's replication is the redundancy. Declare a Talos user volume
  per disk (`Node.LocalVolumes`, mounted at `/var/mnt/<name>`), then one
  PersistentVolume per replica with `charts/local-volumes`, reserved for its
  claim. A pod follows its volume: losing the node loses that replica, which
  the application rebuilds elsewhere. Do not use `hostPath`: a missing mount
  turns it into a directory on the system disk.
- **Longhorn for the rest.** Single-copy workloads that must survive a node
  (home directories, small stateful tools) get replicated block storage from
  Longhorn:
  - the schematic carries `siderolabs/iscsi-tools` and
    `siderolabs/util-linux-tools`;
  - a user volume per node for its data (for example `longhorn`, so
    `/var/mnt/longhorn`), set as Longhorn's `defaultSettings.defaultDataPath`;
    check Talos' Longhorn guide for the kubelet mount it needs on your
    version;
  - its namespace is `privileged` in Pod Security (it runs host-level
    agents), with the reason recorded;
  - under Argo CD, turn off `preUpgradeChecker.jobEnabled` (a Helm hook Argo
    does not run the same way);
  - two replicas per volume is the usual balance for a small cluster; keep
    Longhorn out of nodes whose disks hold the local database volumes if
    their I/O matters.

A third class of disk, slow and large (spinning disks in RAID), fits the
same local-volume pattern: a Talos raw or user volume per array, static
PersistentVolumes on it.

## Conformance

`hack/conformance.sh` (CI: `.github/workflows/conformance.yaml`) is how
this repository checks the provider without Talos hardware:

1. **Offline.** A cluster using every field is rendered with a fresh secrets
   bundle, and every machine config is validated by the real `talosctl`
   (`validate --mode metal --strict`) of the supported version; the issuer
   documents are checked against the contract.
2. **On kind**, standing in for Talos nodes (no CNI, no kube-proxy): Cilium
   with the Talos values; a LoadBalancer Service gets an address from a
   `cilium-config` pool and answers from outside the cluster through L2
   announcements; a `cluster-network-policies` policy denies, then allows;
   a `local-volumes` volume binds on its node and holds data; the
   `cluster-pki` issuer issues a certificate; the `talos-etcd-backup`
   objects pass the API server and Pod Security restricted.

What it cannot cover is the hardware: booting the installer image, the
VIP's failover, user volumes on real disks, the Talos API serving an etcd
snapshot. Those are the first-provisioning checks of a real cluster
(`talosctl health`, a VIP failover drill, a restored snapshot).

## Disaster recovery

- **etcd** is backed up by `charts/talos-etcd-backup`: a CronJob that asks
  the Talos API (role `os:etcd:backup`, granted to its namespace by
  `Cluster.TalosAPIAccess`) for a snapshot, encrypts it to an age recipient
  and uploads it. Keep the age private key in break-glass storage, not in
  the cluster the snapshot describes. Restore when the cluster cannot be
  rebuilt from git and the databases' own backups alone:

  ```sh
  aws s3 cp s3://<bucket>/<prefix>/<cluster>/<snapshot>.age ./db.age
  age --decrypt -i break-glass.key -o db.snapshot db.age
  # every control plane node reset or freshly installed, configs applied;
  # bootstrap ONE of them from the snapshot, the others join it:
  talosctl -n 10.0.0.11 -e 10.0.0.11 bootstrap --recover-from=./db.snapshot
  ```

  Rehearse it on a throwaway cluster: a backup never restored is a hope.
  A snapshot taken by `talosctl etcd snapshot` (not copied from disk) needs
  no `--recover-skip-hash-check`.
- **The secrets bundle** is the other half of a rebuild: with it and the
  declaration, the same cluster (same CAs, same issuer keys) is re-rendered.
- **Workload identity** survives a rebuild unchanged when the bundle does:
  the issuer's keys come from it.
