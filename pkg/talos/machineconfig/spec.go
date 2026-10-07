package machineconfig

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/compatibility"
	"github.com/siderolabs/talos/pkg/machinery/config"

	"github.com/truvity/k8s/pkg/cluster"
	"github.com/truvity/k8s/pkg/talos/schematic"
)

// SupportedTalosMinor is the Talos minor this release renders for. A
// machine config is a versioned contract: a document this release knows may
// be refused by an older Talos and a newer Talos may want documents this
// release does not know, so another minor is refused, not guessed at.
const SupportedTalosMinor = "v1.14"

// Role is a node's role.
type Role string

const (
	// RoleControlPlane runs etcd and the Kubernetes control plane.
	RoleControlPlane Role = "controlplane"
	// RoleWorker runs workloads only.
	RoleWorker Role = "worker"
)

// CNI is the cluster network the machine config installs.
type CNI string

const (
	// CNINone installs no CNI and no kube-proxy manifests' CNI: the caller
	// installs Cilium (see the cilium-config chart) before nodes go Ready.
	CNINone CNI = "none"
	// CNIFlannel keeps Talos' built-in flannel. For a throwaway cluster.
	CNIFlannel CNI = "flannel"
)

// Cluster is everything the machine configs of one cluster are rendered
// from. It holds no secret: the PKI is the secrets bundle, passed to Render
// beside it. Every field is the caller's; nothing defaults to an estate.
type Cluster struct {
	// Name is the cluster's name, a DNS-1123 label. Required.
	Name string `json:"name" yaml:"name"`
	// Endpoint is the Kubernetes API URL every node and client uses:
	// https://<address>:<port>, the address being the control plane VIP or a
	// name that resolves to the control plane nodes. Use an IP when the
	// cluster's DNS may depend on the cluster. Required.
	Endpoint string `json:"endpoint" yaml:"endpoint"`
	// TalosVersion is the Talos release the nodes run, vX.Y.Z, within
	// SupportedTalosMinor. It is the installer image's tag. Required.
	TalosVersion string `json:"talosVersion" yaml:"talosVersion"`
	// KubernetesVersion is the Kubernetes release, X.Y.Z, one Talos
	// supports. Required.
	KubernetesVersion string `json:"kubernetesVersion" yaml:"kubernetesVersion"`

	// Installer is the installer image every node installs and upgrades
	// to, unless the node names its own. Required.
	Installer schematic.Installer `json:"installer" yaml:"installer"`

	// Network is the cluster's address plan and data path.
	Network Network `json:"network" yaml:"network"`
	// ControlPlane is what only the control plane nodes carry.
	ControlPlane ControlPlane `json:"controlPlane" yaml:"controlPlane"`

	// ServiceAccountIssuer is the https URL the API server signs
	// ServiceAccount tokens as, and the URL external systems fetch its keys
	// from (<issuer>/.well-known/openid-configuration and
	// <issuer>/openid/v1/jwks). Set it to a URL that is publicly reachable
	// and serves the documents pkg/talos/oidc generates, to use workload
	// identity. Empty: tokens are issued as Endpoint and nothing outside
	// the cluster can verify them.
	ServiceAccountIssuer string `json:"serviceAccountIssuer,omitempty" yaml:"serviceAccountIssuer,omitempty"`

	// TalosAPIAccess lets ServiceAccounts in the named namespaces reach the
	// Talos API with the named roles (an etcd backup job needs
	// os:etcd:backup). Nil: no pod reaches the Talos API.
	TalosAPIAccess *TalosAPIAccess `json:"talosAPIAccess,omitempty" yaml:"talosAPIAccess,omitempty"`

	// DisableDiscovery turns off Talos' cluster discovery service (the
	// public one by default). Off it, nodes still find each other through
	// Kubernetes, but KubeSpan and the discovery-based health checks lose it.
	DisableDiscovery bool `json:"disableDiscovery,omitempty" yaml:"disableDiscovery,omitempty"`

	// Capabilities is what the contract reports. Empty: api-access, and
	// workload-identity when ServiceAccountIssuer is set. Add the others as
	// the charts that provide them are installed (node-pools is never
	// offered here: nodes are machines, not a scaling group).
	Capabilities []cluster.Capability `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`

	// Patches are Talos machine config patches (strategic merge YAML,
	// several documents allowed) applied to every node after the rendered
	// settings; ControlPlanePatches and WorkerPatches to one role; a node's
	// own Patches last. A later patch wins.
	Patches             []string `json:"patches,omitempty" yaml:"patches,omitempty"`
	ControlPlanePatches []string `json:"controlPlanePatches,omitempty" yaml:"controlPlanePatches,omitempty"`
	WorkerPatches       []string `json:"workerPatches,omitempty" yaml:"workerPatches,omitempty"`

	// Nodes are the cluster's machines. At least one control plane node.
	Nodes []Node `json:"nodes" yaml:"nodes"`
}

// Network is the cluster's address plan and data path.
type Network struct {
	// PodSubnets and ServiceSubnets are the cluster's ranges. Empty: Talos'
	// defaults (10.244.0.0/16 and 10.96.0.0/12).
	PodSubnets     []string `json:"podSubnets,omitempty" yaml:"podSubnets,omitempty"`
	ServiceSubnets []string `json:"serviceSubnets,omitempty" yaml:"serviceSubnets,omitempty"`
	// DNSDomain is the cluster's DNS domain. Empty: cluster.local.
	DNSDomain string `json:"dnsDomain,omitempty" yaml:"dnsDomain,omitempty"`
	// CNI is CNINone (the default) or CNIFlannel.
	CNI CNI `json:"cni,omitempty" yaml:"cni,omitempty"`
	// KubeProxy keeps kube-proxy. False (the default): Cilium replaces it,
	// so the machine config runs none.
	KubeProxy bool `json:"kubeProxy,omitempty" yaml:"kubeProxy,omitempty"`
	// KubePrismPort is the node-local API load balancer's port (Cilium
	// reaches the API through it). Zero: 7445.
	KubePrismPort int `json:"kubePrismPort,omitempty" yaml:"kubePrismPort,omitempty"`
	// NodeSubnets pin the address the kubelet registers and etcd
	// advertises to these ranges. Set them whenever a node has more than
	// one address (a VPN or overlay interface): otherwise either may pick
	// the wrong one, and etcd peers that cannot reach each other split.
	NodeSubnets []string `json:"nodeSubnets,omitempty" yaml:"nodeSubnets,omitempty"`
}

// ControlPlane is what only the control plane nodes carry.
type ControlPlane struct {
	// VIP is a shared layer-2 address the control plane nodes hold one at
	// a time (Talos elects the holder through etcd). Every control plane
	// node then names the link it lives on (Node.VIPLink). Empty: none.
	VIP string `json:"vip,omitempty" yaml:"vip,omitempty"`
	// CertSANs are extra names and addresses on the API server certificate
	// (a DNS name for the VIP, say).
	CertSANs []string `json:"certSANs,omitempty" yaml:"certSANs,omitempty"`
	// AllowScheduling lets workloads run on control plane nodes (no
	// NoSchedule taint). A node's own Taints can still set a softer one.
	AllowScheduling bool `json:"allowScheduling,omitempty" yaml:"allowScheduling,omitempty"`
}

// TalosAPIAccess is which ServiceAccounts may reach the Talos API.
type TalosAPIAccess struct {
	// Roles are Talos API roles, for example os:etcd:backup or os:reader.
	Roles []string `json:"roles" yaml:"roles"`
	// Namespaces are the Kubernetes namespaces whose ServiceAccounts may
	// ask for those roles (through a talos.dev ServiceAccount object).
	Namespaces []string `json:"namespaces" yaml:"namespaces"`
}

// Node is one machine.
type Node struct {
	// Hostname is the node's name, a DNS-1123 label, unique. Required.
	Hostname string `json:"hostname" yaml:"hostname"`
	// Role is RoleControlPlane or RoleWorker. Required.
	Role Role `json:"role" yaml:"role"`
	// Address is the IP talosctl reaches the node at; the control plane
	// nodes' addresses are the talosconfig's endpoints. Required.
	Address string `json:"address" yaml:"address"`
	// InstallDisk is the device Talos installs to (/dev/nvme0n1), or
	// InstallDiskSelector a CEL expression over the disk (for example
	// `disk.transport == "nvme"`). Exactly one is required.
	InstallDisk         string `json:"installDisk,omitempty" yaml:"installDisk,omitempty"`
	InstallDiskSelector string `json:"installDiskSelector,omitempty" yaml:"installDiskSelector,omitempty"`
	// Installer replaces the cluster's installer for this node (a board
	// that needs its own overlay or its own build).
	Installer *schematic.Installer `json:"installer,omitempty" yaml:"installer,omitempty"`
	// VIPLink is the link the control plane VIP lives on (eth0, end0, a
	// bond). Required on a control plane node when ControlPlane.VIP is set.
	VIPLink string `json:"vipLink,omitempty" yaml:"vipLink,omitempty"`
	// Labels and Taints are the Kubernetes node's. A taint's value is
	// "Effect" or "value:Effect". Talos sets them at registration; a
	// change on a registered node needs kubectl once (NodeRestriction).
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Taints map[string]string `json:"taints,omitempty" yaml:"taints,omitempty"`
	// LocalVolumes are Talos user volumes, each mounted at
	// /var/mnt/<name>: the disks local PersistentVolumes live on.
	LocalVolumes []LocalVolume `json:"localVolumes,omitempty" yaml:"localVolumes,omitempty"`
	// Patches are this node's own machine config patches, applied last.
	Patches []string `json:"patches,omitempty" yaml:"patches,omitempty"`
}

// LocalVolume is a Talos user volume (UserVolumeConfig).
type LocalVolume struct {
	// Name is the volume's name, mounted at /var/mnt/<name>. Required.
	Name string `json:"name" yaml:"name"`
	// DiskSelector is a CEL expression choosing the disk the volume is
	// carved from, for example `disk.transport == "nvme" && !system_disk`.
	// Required.
	DiskSelector string `json:"diskSelector" yaml:"diskSelector"`
	// MinSize and MaxSize bound the partition ("100GiB", "80%"); at least
	// one is required. With no MaxSize the volume takes the rest of the
	// disk.
	MinSize string `json:"minSize,omitempty" yaml:"minSize,omitempty"`
	MaxSize string `json:"maxSize,omitempty" yaml:"maxSize,omitempty"`
	// Filesystem is xfs (the default) or ext4.
	Filesystem string `json:"filesystem,omitempty" yaml:"filesystem,omitempty"`
	// Encryption is "" (none), "tpm" (sealed to the node's TPM) or "nodeID"
	// (derived from the node's identity: protects against a disk taken
	// away, not against the node). A static key is a secret: use a patch.
	Encryption string `json:"encryption,omitempty" yaml:"encryption,omitempty"`
}

var (
	dnsLabel    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	talosSemver = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	k8sSemver   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	sizePattern = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?\s*([KMGTPE]i?B|B)?|[0-9]{1,3}%)$`)
)

// TalosAPIRoles are the Talos API roles TalosAPIAccess may grant.
func TalosAPIRoles() []string {
	return []string{"os:admin", "os:operator", "os:reader", "os:etcd:backup", "os:impersonator"}
}

// Validate refuses a cluster the renderer could only guess at. It reports
// every problem, not the first.
func (c *Cluster) Validate() error {
	var errs []error

	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !dnsLabel.MatchString(c.Name) {
		add("name %q is not a DNS-1123 label", c.Name)
	}

	if u, err := url.Parse(c.Endpoint); err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() == "" || (u.Path != "" && u.Path != "/") {
		add("endpoint %q is not https://<host>:<port>", c.Endpoint)
	}

	errs = append(errs, c.checkVersions()...)

	if err := c.Installer.Validate(); err != nil {
		add("cluster %w", err)
	}

	errs = append(errs, c.Network.check()...)

	if v := c.ControlPlane.VIP; v != "" {
		if _, err := netip.ParseAddr(v); err != nil {
			add("controlPlane.vip %q is not an IP address", v)
		}
	}

	if c.ServiceAccountIssuer != "" {
		u, err := url.Parse(c.ServiceAccountIssuer)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(u.Path, "/") {
			add("serviceAccountIssuer %q is not an https URL without a trailing slash", c.ServiceAccountIssuer)
		}
	}

	if a := c.TalosAPIAccess; a != nil {
		if len(a.Roles) == 0 || len(a.Namespaces) == 0 {
			add("talosAPIAccess needs at least one role and one namespace")
		}

		for _, r := range a.Roles {
			if !slices.Contains(TalosAPIRoles(), r) {
				add("talosAPIAccess: unknown role %q", r)
			}
		}

		for _, n := range a.Namespaces {
			if !dnsLabel.MatchString(n) {
				add("talosAPIAccess: namespace %q is not a DNS-1123 label", n)
			}
		}
	}

	for _, cp := range c.Capabilities {
		if !slices.Contains(cluster.All(), cp) {
			add("capabilities: unknown capability %q", cp)
		}
	}

	errs = append(errs, c.checkNodes()...)

	return errors.Join(errs...)
}

func (c *Cluster) checkVersions() []error {
	var errs []error

	if !talosSemver.MatchString(c.TalosVersion) {
		return append(errs, fmt.Errorf("talosVersion %q is not vX.Y.Z", c.TalosVersion))
	}

	if minor := c.TalosVersion[:strings.LastIndex(c.TalosVersion, ".")]; minor != SupportedTalosMinor {
		errs = append(errs, fmt.Errorf("talosVersion %s: this release renders for Talos %s.x only", c.TalosVersion, SupportedTalosMinor))
	}

	if _, err := config.ParseContractFromVersion(c.TalosVersion); err != nil {
		errs = append(errs, fmt.Errorf("talosVersion %q: %w", c.TalosVersion, err))
	}

	if !k8sSemver.MatchString(c.KubernetesVersion) {
		return append(errs, fmt.Errorf("kubernetesVersion %q is not X.Y.Z (no leading v)", c.KubernetesVersion))
	}

	talos, err := compatibility.ParseTalosVersion(&machine.VersionInfo{Tag: c.TalosVersion})
	if err != nil {
		return append(errs, fmt.Errorf("talosVersion %q: %w", c.TalosVersion, err))
	}

	k8s, err := compatibility.ParseKubernetesVersion(c.KubernetesVersion)
	if err != nil {
		return append(errs, fmt.Errorf("kubernetesVersion %q: %w", c.KubernetesVersion, err))
	}

	if err := k8s.SupportedWith(talos); err != nil {
		errs = append(errs, fmt.Errorf("kubernetesVersion %s with Talos %s: %w", c.KubernetesVersion, c.TalosVersion, err))
	}

	return errs
}

func (n *Network) check() []error {
	var errs []error

	for field, list := range map[string][]string{"podSubnets": n.PodSubnets, "serviceSubnets": n.ServiceSubnets, "nodeSubnets": n.NodeSubnets} {
		for _, s := range list {
			if p, err := netip.ParsePrefix(s); err != nil || p.Masked() != p {
				errs = append(errs, fmt.Errorf("network.%s: %q is not a CIDR without host bits", field, s))
			}
		}
	}

	switch n.CNI {
	case "", CNINone, CNIFlannel:
	default:
		errs = append(errs, fmt.Errorf("network.cni %q is neither %q nor %q", n.CNI, CNINone, CNIFlannel))
	}

	if n.KubePrismPort < 0 || n.KubePrismPort > 65535 {
		errs = append(errs, fmt.Errorf("network.kubePrismPort %d is not a port", n.KubePrismPort))
	}

	if n.CNI == CNIFlannel && !n.KubeProxy {
		errs = append(errs, errors.New("network: flannel needs kube-proxy (kubeProxy: true)"))
	}

	if n.DNSDomain != "" && !regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`).MatchString(n.DNSDomain) {
		errs = append(errs, fmt.Errorf("network.dnsDomain %q is not a domain", n.DNSDomain))
	}

	return errs
}

func (c *Cluster) checkNodes() []error {
	var errs []error

	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	hostnames, addresses := map[string]bool{}, map[string]bool{}
	controlPlanes := 0

	for i := range c.Nodes {
		n := &c.Nodes[i]
		where := fmt.Sprintf("nodes[%d] (%s)", i, n.Hostname)

		switch {
		case !dnsLabel.MatchString(n.Hostname):
			add("nodes[%d]: hostname %q is not a DNS-1123 label", i, n.Hostname)
		case hostnames[n.Hostname]:
			add("%s: hostname repeated", where)
		}

		hostnames[n.Hostname] = true

		switch n.Role {
		case RoleControlPlane:
			controlPlanes++

			if c.ControlPlane.VIP != "" && n.VIPLink == "" {
				add("%s: controlPlane.vip is set, so a control plane node needs vipLink", where)
			}
		case RoleWorker:
			if n.VIPLink != "" {
				add("%s: vipLink on a worker", where)
			}
		default:
			add("%s: role %q is neither %q nor %q", where, n.Role, RoleControlPlane, RoleWorker)
		}

		if _, err := netip.ParseAddr(n.Address); err != nil {
			add("%s: address %q is not an IP address", where, n.Address)
		} else if addresses[n.Address] {
			add("%s: address %s repeated", where, n.Address)
		}

		addresses[n.Address] = true

		if (n.InstallDisk == "") == (n.InstallDiskSelector == "") {
			add("%s: exactly one of installDisk and installDiskSelector is required", where)
		}

		if n.InstallDisk != "" && !strings.HasPrefix(n.InstallDisk, "/dev/") {
			add("%s: installDisk %q is not a /dev path", where, n.InstallDisk)
		}

		if n.Installer != nil {
			if err := n.Installer.Validate(); err != nil {
				add("%s: %w", where, err)
			}
		}

		errs = append(errs, checkVolumes(where, n.LocalVolumes)...)
	}

	if controlPlanes == 0 {
		add("nodes: no control plane node")
	}

	return errs
}

func checkVolumes(where string, vols []LocalVolume) []error {
	var errs []error

	names := map[string]bool{}

	for j, v := range vols {
		w := fmt.Sprintf("%s: localVolumes[%d] (%s)", where, j, v.Name)

		switch {
		case !dnsLabel.MatchString(v.Name):
			errs = append(errs, fmt.Errorf("%s: name is not a DNS-1123 label", w))
		case names[v.Name]:
			errs = append(errs, fmt.Errorf("%s: name repeated", w))
		}

		names[v.Name] = true

		if strings.TrimSpace(v.DiskSelector) == "" {
			errs = append(errs, fmt.Errorf("%s: diskSelector is required", w))
		}

		for field, s := range map[string]string{"minSize": v.MinSize, "maxSize": v.MaxSize} {
			if s != "" && !sizePattern.MatchString(s) {
				errs = append(errs, fmt.Errorf("%s: %s %q is not a size (100GiB) or a percentage (80%%)", w, field, s))
			}
		}

		if v.MinSize == "" && v.MaxSize == "" {
			errs = append(errs, fmt.Errorf("%s: minSize or maxSize is required", w))
		}

		switch v.Filesystem {
		case "", "xfs", "ext4":
		default:
			errs = append(errs, fmt.Errorf("%s: filesystem %q is neither xfs nor ext4", w, v.Filesystem))
		}

		switch v.Encryption {
		case "", "tpm", "nodeID":
		default:
			errs = append(errs, fmt.Errorf("%s: encryption %q is none of \"\", tpm, nodeID", w, v.Encryption))
		}
	}

	return errs
}
