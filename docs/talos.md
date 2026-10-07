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

## Disaster recovery

- **etcd** is backed up by `charts/talos-etcd-backup`: a CronJob that asks
  the Talos API (role `os:etcd:backup`, granted to its namespace by
  `Cluster.TalosAPIAccess`) for a snapshot, encrypts it to an age recipient
  and uploads it. Keep the age private key in break-glass storage, not in
  the cluster the snapshot describes. Restore when the cluster cannot be
  rebuilt from git and the databases' own backups alone:

  ```sh
  aws s3 cp s3://<bucket>/<prefix>/<cluster>/<snapshot>.age ./db.age
  age --decrypt -i break-glass.key -o db.snapshot db.age   # (zstd -d first or after, if compressed)
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
