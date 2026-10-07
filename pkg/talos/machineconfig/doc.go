// Package machineconfig renders the Talos machine configs of a self-hosted
// cluster from a declared Cluster and its secrets bundle, offline, with
// Talos' own config machinery: the same generator talosctl uses, then the
// Cluster's settings as a patch, then the caller's patches (cluster-wide,
// per role, per node), then Talos' client-side validation in metal mode.
//
// What the Cluster's fields set, beyond talosctl's defaults:
//
//   - the installer image, pinned to an Image Factory schematic ID and the
//     Talos version (pkg/talos/schematic), on an install disk named by
//     device path or by a CEL selector;
//   - no CNI and no kube-proxy by default, for Cilium with its kube-proxy
//     replacement reaching the API through KubePrism;
//   - the ServiceAccount issuer and its JWKS URI, for workload identity
//     against a public issuer (pkg/talos/oidc publishes the documents);
//   - a shared layer-2 VIP on the control plane, the node subnets the
//     kubelet and etcd must use, node labels and taints;
//   - user volumes for local PersistentVolumes;
//   - Talos API access for ServiceAccounts (an etcd backup job).
//
// The renderer knows one Talos minor (SupportedTalosMinor) and refuses
// another: a machine config is a versioned contract.
//
// Render is deterministic in the Cluster and the bundle (the talosconfig's
// client certificate excepted). RenderedPatch shows, without secrets, what
// the Cluster's fields turn into for a node. Contract reports the cluster
// under pkg/cluster's provider-neutral contract.
package machineconfig
