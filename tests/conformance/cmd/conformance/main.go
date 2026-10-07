// Command conformance prepares the inputs of hack/conformance.sh: a Talos
// cluster's machine configs rendered with a fresh secrets bundle (checked by
// the real talosctl), its issuer documents, and Cilium's values for a kind
// cluster standing in for Talos nodes.
//
//	conformance talos-render <dir>
//	conformance cilium-values <api-host> <api-port>
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"go.yaml.in/yaml/v4"

	"github.com/truvity/k8s/pkg/cluster"
	"github.com/truvity/k8s/pkg/talos/cilium"
	"github.com/truvity/k8s/pkg/talos/machineconfig"
	"github.com/truvity/k8s/pkg/talos/oidc"
	"github.com/truvity/k8s/pkg/talos/schematic"
)

const talosVersion = "v1.14.2"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "talos-render":
		return talosRender(args[1])
	case len(args) == 3 && args[0] == "cilium-values":
		port, err := strconv.Atoi(args[2])
		if err != nil {
			return err
		}

		return ciliumValues(args[1], port)
	default:
		return errors.New("usage: conformance talos-render <dir> | cilium-values <api-host> <api-port>")
	}
}

// example is a greenfield cluster using every field the renderer has.
func example() *machineconfig.Cluster {
	s := &schematic.Schematic{Customization: schematic.Customization{SystemExtensions: schematic.SystemExtensions{
		OfficialExtensions: []string{"siderolabs/iscsi-tools", "siderolabs/util-linux-tools"},
	}}}

	id, err := s.ID()
	if err != nil {
		panic(err)
	}

	cp := func(name, addr string) machineconfig.Node {
		return machineconfig.Node{Hostname: name, Role: machineconfig.RoleControlPlane, Address: addr, InstallDisk: "/dev/nvme0n1", VIPLink: "eth0"}
	}

	return &machineconfig.Cluster{
		Name:                 "conformance",
		Endpoint:             "https://10.0.0.10:6443",
		TalosVersion:         talosVersion,
		KubernetesVersion:    "1.36.4",
		Installer:            schematic.Installer{SchematicID: id},
		Network:              machineconfig.Network{NodeSubnets: []string{"10.0.0.0/24"}},
		ControlPlane:         machineconfig.ControlPlane{VIP: "10.0.0.10", CertSANs: []string{"api.example.com"}},
		ServiceAccountIssuer: "https://oidc.example.com/conformance",
		TalosAPIAccess:       &machineconfig.TalosAPIAccess{Roles: []string{"os:etcd:backup"}, Namespaces: []string{"etcd-backup"}},
		Capabilities: []cluster.Capability{
			cluster.APIAccess, cluster.WorkloadIdentity, cluster.NetworkPolicy, cluster.LoadBalancing, cluster.Storage,
		},
		Nodes: []machineconfig.Node{
			cp("cp-1", "10.0.0.11"), cp("cp-2", "10.0.0.12"), cp("cp-3", "10.0.0.13"),
			{
				Hostname: "db-1", Role: machineconfig.RoleWorker, Address: "10.0.0.21", InstallDiskSelector: `disk.transport == "nvme"`,
				LocalVolumes: []machineconfig.LocalVolume{
					{Name: "local-db", DiskSelector: `disk.transport == "nvme" && !system_disk`, MinSize: "100GiB"},
					{Name: "longhorn", DiskSelector: `disk.rotational`, MinSize: "100GiB"},
				},
			},
		},
	}
}

func talosRender(dir string) error {
	c := example()

	bundle, err := machineconfig.NewSecrets(talosVersion)
	if err != nil {
		return err
	}

	out, err := machineconfig.Render(c, bundle)
	if err != nil {
		return err
	}

	if err := out.Contract.Validate(); err != nil {
		return fmt.Errorf("contract: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	files := map[string][]byte{"talosconfig": out.Talosconfig}
	for host, data := range out.Nodes {
		files[host+".machine.yaml"] = data
	}

	keys, err := oidc.PublicKeysFromPEM(bundle.Certs.K8sServiceAccount.Key)
	if err != nil {
		return err
	}

	docs, err := oidc.Generate(c.ServiceAccountIssuer, keys...)
	if err != nil {
		return err
	}

	files["openid-configuration.json"] = docs.Discovery
	files["jwks.json"] = docs.JWKS

	contract, err := json.MarshalIndent(out.Contract, "", "  ")
	if err != nil {
		return err
	}

	files["contract.json"] = contract

	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}

	for _, w := range out.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	fmt.Printf("rendered %d machine configs to %s\n", len(out.Nodes), dir)

	return nil
}

// ciliumValues prints the Talos values with the API pointed at kind's
// control plane: on Talos, KubePrism serves it at localhost:7445.
func ciliumValues(host string, port int) error {
	v, err := cilium.Values(cilium.Options{
		OperatorReplicas: 1,
		Overrides:        map[string]any{"k8sServiceHost": host, "k8sServicePort": port},
	})
	if err != nil {
		return err
	}

	out, err := yaml.Marshal(v)
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(out)

	return err
}
