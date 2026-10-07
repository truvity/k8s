package machineconfig

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	coreconfig "github.com/siderolabs/talos/pkg/machinery/config"
	configdoc "github.com/siderolabs/talos/pkg/machinery/config/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	"github.com/siderolabs/talos/pkg/machinery/config/container"
	"github.com/siderolabs/talos/pkg/machinery/config/encoder"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/config/validation"
	"go.yaml.in/yaml/v4"

	"github.com/truvity/k8s/pkg/cluster"
)

// Rendered is the output of Render.
type Rendered struct {
	// Nodes is each node's machine config, by hostname: what
	// `talosctl apply-config --file` takes. It holds the cluster's secrets.
	Nodes map[string][]byte
	// Talosconfig is an os:admin talosctl client config whose endpoints are
	// the control plane nodes' addresses. It holds a client key.
	Talosconfig []byte
	// Contract is the cluster under the provider-neutral contract.
	Contract cluster.Outputs
	// Warnings are what Talos' validator and this renderer advise against
	// without refusing it.
	Warnings []string
}

// NewSecrets generates a cluster's PKI and secrets (the Talos, Kubernetes,
// aggregator and etcd CAs, the ServiceAccount signing key, the bootstrap
// token, the etcd encryption secret, the cluster ID) for a Talos version.
// Generate it ONCE per cluster and keep it like a root key: every machine
// config is rendered from it, and a cluster cannot be re-rendered without
// it. The ServiceAccount key is RSA: AWS does not accept tokens signed with
// an ECDSA key for IAM roles for service accounts.
func NewSecrets(talosVersion string) (*secrets.Bundle, error) {
	contract, err := coreconfig.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}

	return secrets.NewBundle(secrets.NewClock(), contract)
}

// MarshalSecrets is the bundle in talosctl's secrets.yaml format.
func MarshalSecrets(b *secrets.Bundle) ([]byte, error) { return yaml.Marshal(b) }

// ParseSecrets reads a bundle in talosctl's secrets.yaml format (what
// `talosctl gen secrets` writes and MarshalSecrets returns).
func ParseSecrets(data []byte) (*secrets.Bundle, error) {
	b := &secrets.Bundle{Clock: secrets.NewClock()}
	if err := yaml.Unmarshal(data, b); err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}

	if b.Certs == nil || b.Certs.K8s == nil || b.Certs.OS == nil || b.Certs.K8sServiceAccount == nil || b.Secrets == nil || b.Cluster == nil {
		return nil, errors.New("secrets: the bundle is incomplete")
	}

	return b, nil
}

// metal is Talos' metal runtime mode, for client-side validation: the
// node installs to disk and runs on bare metal (or a VM booted like it).
type metal struct{}

func (metal) String() string        { return "metal" }
func (metal) RequiresInstall() bool { return true }
func (metal) InContainer() bool     { return false }

// Render renders every node's machine config, the talosconfig and the
// contract from the cluster and its secrets. It is deterministic in its
// inputs except for the talosconfig's client certificate, which is minted
// fresh. Nothing is written and nothing is contacted.
func Render(c *Cluster, bundle *secrets.Bundle) (*Rendered, error) {
	if c == nil || bundle == nil {
		return nil, errors.New("machineconfig: cluster and secrets are required")
	}

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("machineconfig %s: %w", c.Name, err)
	}

	out := &Rendered{Nodes: map[string][]byte{}}

	var cpAddresses []string

	for i := range c.Nodes {
		if c.Nodes[i].Role == RoleControlPlane {
			cpAddresses = append(cpAddresses, c.Nodes[i].Address)
		}
	}

	if len(cpAddresses)%2 == 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"%d control plane nodes: etcd keeps quorum through as many failures as one node fewer; use an odd number", len(cpAddresses)))
	}

	for i := range c.Nodes {
		n := &c.Nodes[i]

		data, warnings, err := c.renderNode(n, bundle, cpAddresses)
		if err != nil {
			return nil, fmt.Errorf("machineconfig %s: node %s: %w", c.Name, n.Hostname, err)
		}

		out.Nodes[n.Hostname] = data

		for _, w := range warnings {
			out.Warnings = append(out.Warnings, n.Hostname+": "+w)
		}
	}

	image, err := c.Installer.Reference(c.TalosVersion)
	if err != nil {
		return nil, err
	}

	in, err := c.input(bundle, cpAddresses, image)
	if err != nil {
		return nil, err
	}

	tc, err := in.Talosconfig()
	if err != nil {
		return nil, fmt.Errorf("machineconfig %s: talosconfig: %w", c.Name, err)
	}

	if out.Talosconfig, err = tc.Bytes(); err != nil {
		return nil, fmt.Errorf("machineconfig %s: talosconfig: %w", c.Name, err)
	}

	out.Contract = c.Contract(bundle)

	return out, nil
}

// Contract is the cluster under the provider-neutral contract: provider
// talos, the name, the endpoint, the Kubernetes CA from the bundle, the
// ServiceAccount issuer and the capabilities.
func (c *Cluster) Contract(bundle *secrets.Bundle) cluster.Outputs {
	caps := c.Capabilities
	if len(caps) == 0 {
		caps = []cluster.Capability{cluster.APIAccess}
		if c.ServiceAccountIssuer != "" {
			caps = append(caps, cluster.WorkloadIdentity)
		}
	}

	set := cluster.NewCapabilities(caps...)

	out := cluster.Outputs{
		Provider:     cluster.ProviderTalos,
		Name:         c.Name,
		Endpoint:     c.Endpoint,
		Capabilities: set,
	}

	if set.Has(cluster.WorkloadIdentity) {
		out.OIDCIssuer = c.ServiceAccountIssuer
	}

	if bundle != nil && bundle.Certs != nil && bundle.Certs.K8s != nil {
		out.CertificateAuthorityPEM = string(bundle.Certs.K8s.Crt)
	}

	return out
}

func (c *Cluster) input(bundle *secrets.Bundle, cpAddresses []string, image string) (*generate.Input, error) {
	contract, err := coreconfig.ParseContractFromVersion(c.TalosVersion)
	if err != nil {
		return nil, err
	}

	opts := []generate.Option{
		generate.WithVersionContract(contract),
		generate.WithSecretsBundle(bundle),
		generate.WithEndpointList(cpAddresses),
		generate.WithInstallImage(image),
		// The installer document is rendered below, from either a device
		// path or a selector; the generated one only knows a path.
		generate.WithSkipUnattendedInstallConfig(true),
		generate.WithAllowSchedulingOnControlPlanes(c.ControlPlane.AllowScheduling),
		generate.WithClusterDiscovery(!c.DisableDiscovery),
	}

	if sans := c.ControlPlane.CertSANs; len(sans) > 0 {
		opts = append(opts, generate.WithAdditionalSubjectAltNames(sans))
	}

	if c.Network.KubePrismPort != 0 {
		opts = append(opts, generate.WithKubePrismPort(c.Network.KubePrismPort))
	}

	if d := c.Network.DNSDomain; d != "" {
		opts = append(opts, generate.WithDNSDomain(d))
	}

	if c.Network.CNI != CNIFlannel {
		// A custom CNI URL is what makes the generator leave flannel out;
		// the external manifest document it adds instead is removed again
		// below, so no CNI is installed at all.
		opts = append(opts, generate.WithCustomCNIUrl("https://cni.invalid/none.yaml"))
	}

	in, err := generate.NewInput(c.Name, c.Endpoint, c.KubernetesVersion, opts...)
	if err != nil {
		return nil, err
	}

	if len(c.Network.PodSubnets) > 0 {
		in.PodNet = slices.Clone(c.Network.PodSubnets)
	}

	if len(c.Network.ServiceSubnets) > 0 {
		in.ServiceNet = slices.Clone(c.Network.ServiceSubnets)
	}

	return in, nil
}

func (c *Cluster) renderNode(n *Node, bundle *secrets.Bundle, cpAddresses []string) ([]byte, []string, error) {
	inst := c.Installer
	if n.Installer != nil {
		inst = *n.Installer
	}

	image, err := inst.Reference(c.TalosVersion)
	if err != nil {
		return nil, nil, err
	}

	in, err := c.input(bundle, cpAddresses, image)
	if err != nil {
		return nil, nil, err
	}

	typ := machine.TypeWorker
	if n.Role == RoleControlPlane {
		typ = machine.TypeControlPlane
	}

	generated, err := in.Config(typ)
	if err != nil {
		return nil, nil, fmt.Errorf("generate: %w", err)
	}

	cfg, err := dropDocuments(generated, func(kind, name string) bool {
		switch kind {
		case "KubeExternalManifestConfig":
			return c.Network.CNI != CNIFlannel && name == "custom-cni"
		case "HostnameConfig":
			return true // replaced by the node's own hostname below
		}

		return false
	})
	if err != nil {
		return nil, nil, err
	}

	patches := []string{c.renderedPatch(n, image)}
	patches = append(patches, c.Patches...)

	if n.Role == RoleControlPlane {
		patches = append(patches, c.ControlPlanePatches...)
	} else {
		patches = append(patches, c.WorkerPatches...)
	}

	patches = append(patches, n.Patches...)

	loaded, err := configpatcher.LoadPatches(patches)
	if err != nil {
		return nil, nil, fmt.Errorf("load patches: %w", err)
	}

	patched, err := configpatcher.Apply(configpatcher.WithConfig(cfg), loaded)
	if err != nil {
		return nil, nil, fmt.Errorf("apply patches: %w", err)
	}

	final, err := patched.Config()
	if err != nil {
		return nil, nil, fmt.Errorf("apply patches: %w", err)
	}

	warnings, err := validate(final)
	if err != nil {
		return nil, nil, fmt.Errorf("validate: %w", err)
	}

	data, err := final.EncodeBytes(encoder.WithComments(encoder.CommentsDisabled))
	if err != nil {
		return nil, nil, fmt.Errorf("encode: %w", err)
	}

	return data, warnings, nil
}

func validate(cfg coreconfig.Provider) ([]string, error) {
	v, ok := cfg.(interface {
		ValidateAsClient(validation.RuntimeMode, ...validation.Option) ([]string, error)
	})
	if !ok {
		return nil, fmt.Errorf("config of type %T cannot be validated", cfg)
	}

	return v.ValidateAsClient(metal{}, validation.WithStrict())
}

// dropDocuments returns cfg without the documents drop selects.
func dropDocuments(cfg coreconfig.Provider, drop func(kind, name string) bool) (coreconfig.Provider, error) {
	var keep []configdoc.Document

	for _, d := range cfg.Documents() {
		name := ""
		if named, ok := d.(interface{ Name() string }); ok {
			name = named.Name()
		}

		if !drop(d.Kind(), name) {
			keep = append(keep, d)
		}
	}

	return container.New(keep...)
}

// renderedPatch is the patch the Cluster's own fields make, applied before
// the caller's patches. It is plain machine config YAML, so it reads like
// any patch a caller would write.
func (c *Cluster) renderedPatch(n *Node, image string) string {
	var docs []map[string]any

	doc := func(kind string, fields map[string]any) {
		d := map[string]any{"apiVersion": "v1alpha1", "kind": kind}
		for k, v := range fields {
			d[k] = v
		}

		docs = append(docs, d)
	}

	// The legacy v1alpha1 document still carries etcd's settings.
	if n.Role == RoleControlPlane && len(c.Network.NodeSubnets) > 0 {
		docs = append(docs, map[string]any{
			"version": "v1alpha1",
			"cluster": map[string]any{"etcd": map[string]any{"advertisedSubnets": c.Network.NodeSubnets}},
		})
	}

	selector := n.InstallDiskSelector
	if selector == "" {
		selector = fmt.Sprintf("disk.dev_path == %q", n.InstallDisk)
	}

	doc("UnattendedInstallConfig", map[string]any{
		"installer":    map[string]any{"image": image},
		"provisioning": map[string]any{"diskSelector": map[string]any{"match": selector}, "wipe": false},
	})

	doc("HostnameConfig", map[string]any{"hostname": n.Hostname})

	node := map[string]any{}
	if len(c.Network.NodeSubnets) > 0 {
		node["nodeIP"] = map[string]any{"validSubnets": c.Network.NodeSubnets}
	}

	if len(n.Labels) > 0 {
		node["labels"] = n.Labels
	}

	if len(n.Taints) > 0 {
		node["taints"] = n.Taints
	}

	if len(node) > 0 {
		doc("KubeNodeConfig", node)
	}

	if n.Role == RoleControlPlane {
		// kube-proxy is a control plane document: it renders the DaemonSet.
		if !c.Network.KubeProxy {
			doc("KubeProxyConfig", map[string]any{"enabled": false})
		}

		if c.ServiceAccountIssuer != "" {
			// The issuer signs; the endpoint stays accepted, so tokens minted
			// before the issuer was set (and in-cluster clients that pinned
			// it) keep working.
			doc("KubeServiceAccountConfig", map[string]any{
				"issuer":   map[string]any{"issuerURL": c.ServiceAccountIssuer},
				"accepted": map[string]any{"issuers": []string{c.Endpoint}},
			})
			doc("KubeAPIServerConfig", map[string]any{
				"extraArgs": map[string]any{"service-account-jwks-uri": jwksURI(c.ServiceAccountIssuer)},
			})
		}

		if c.ControlPlane.VIP != "" {
			doc("Layer2VIPConfig", map[string]any{"name": c.ControlPlane.VIP, "link": n.VIPLink})
		}

		if a := c.TalosAPIAccess; a != nil {
			doc("KubeTalosAPIAccessConfig", map[string]any{
				"allowedRoles":                sorted(a.Roles),
				"allowedKubernetesNamespaces": sorted(a.Namespaces),
			})
		}
	}

	for _, v := range n.LocalVolumes {
		doc("UserVolumeConfig", userVolume(v))
	}

	var b strings.Builder

	for i, d := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}

		b.Write(marshalDoc(d))
	}

	return b.String()
}

// marshalDoc renders a document with its header keys (version, or
// apiVersion and kind, then name) first and the rest in key order, the way
// Talos' own documents read.
func marshalDoc(d map[string]any) []byte {
	var node yaml.Node
	if err := node.Encode(d); err != nil {
		// Maps of strings, bools, ints and string slices always encode.
		panic(err)
	}

	rank := map[string]int{"version": 0, "apiVersion": 1, "kind": 2, "name": 3}
	pairs := make([][2]*yaml.Node, 0, len(node.Content)/2)

	for i := 0; i+1 < len(node.Content); i += 2 {
		pairs = append(pairs, [2]*yaml.Node{node.Content[i], node.Content[i+1]})
	}

	sort.SliceStable(pairs, func(i, j int) bool {
		ri, iok := rank[pairs[i][0].Value]
		rj, jok := rank[pairs[j][0].Value]

		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		default:
			return pairs[i][0].Value < pairs[j][0].Value
		}
	})

	node.Content = node.Content[:0]
	for _, p := range pairs {
		node.Content = append(node.Content, p[0], p[1])
	}

	out, err := yaml.Marshal(&node)
	if err != nil {
		panic(err)
	}

	return out
}

func userVolume(v LocalVolume) map[string]any {
	prov := map[string]any{"diskSelector": map[string]any{"match": v.DiskSelector}}
	if v.MinSize != "" {
		prov["minSize"] = v.MinSize
	}

	if v.MaxSize != "" {
		prov["maxSize"] = v.MaxSize
	}

	fs := v.Filesystem
	if fs == "" {
		fs = "xfs"
	}

	out := map[string]any{"name": v.Name, "provisioning": prov, "filesystem": map[string]any{"type": fs}}

	switch v.Encryption {
	case "tpm":
		out["encryption"] = map[string]any{"provider": "luks2", "keys": []any{map[string]any{"slot": 0, "tpm": map[string]any{}}}}
	case "nodeID":
		out["encryption"] = map[string]any{"provider": "luks2", "keys": []any{map[string]any{"slot": 0, "nodeID": map[string]any{}}}}
	}

	return out
}

// jwksURI is where the issuer's keys are published, the path the API
// server serves them at itself.
func jwksURI(issuer string) string {
	u, _ := url.Parse(issuer) //nolint:errcheck // Validate parsed it

	return strings.TrimSuffix(u.String(), "/") + "/openid/v1/jwks"
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)

	return out
}

// RenderedPatch is the patch Render applies for a node before the caller's
// patches, exported so a reviewer (or a golden test) can see exactly what
// the Cluster's fields turn into. It holds no secret.
func (c *Cluster) RenderedPatch(hostname string) (string, error) {
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if n.Hostname != hostname {
			continue
		}

		inst := c.Installer
		if n.Installer != nil {
			inst = *n.Installer
		}

		image, err := inst.Reference(c.TalosVersion)
		if err != nil {
			return "", err
		}

		return c.renderedPatch(n, image), nil
	}

	return "", fmt.Errorf("no node %q", hostname)
}
