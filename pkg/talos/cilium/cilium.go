// Package cilium renders the upstream Cilium chart's values for a Talos
// cluster: the settings Talos requires (no cgroup automount, the host's
// cgroup root, the agent's capability list, the API through KubePrism) plus
// the features this repository's cilium-config chart configures (L2
// announcements, BGP) and the data path the caller chooses.
//
// Install Cilium with the result before the nodes can go Ready (the machine
// config renders no CNI), then hand it to whatever manages the cluster's
// releases with the same values:
//
//	helm install cilium cilium/cilium --version <pinned> -n kube-system -f values.yaml
//
// The values are the upstream chart's; an option this package does not
// model is the caller's to merge on top (Overrides).
package cilium

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
)

// DefaultKubePrismPort is Talos' node-local API load balancer port.
const DefaultKubePrismPort = 7445

// Options are the choices a cluster makes; the zero value is a working
// Talos cluster with L2 announcements on and tunnelled routing.
type Options struct {
	// KubePrismPort is the port of Talos' node-local API load balancer.
	// Zero: DefaultKubePrismPort. Cilium reaches the API through it, so it
	// works before any Service does.
	KubePrismPort int
	// NativeRoutingCIDR switches routing from VXLAN tunnels to native
	// routing with direct node routes, for nodes on one layer-2 segment:
	// the pod CIDR (10.244.0.0/16, say). Empty: tunnelled.
	NativeRoutingCIDR string
	// Devices are the interfaces Cilium attaches its datapath to (a regex
	// list such as "eth+"). Name them whenever a node has a VPN or overlay
	// link, or Cilium may pick it and lower the pod MTU. Empty: Cilium
	// detects them.
	Devices []string
	// DisableL2Announcements turns L2 announcements off (BGP or an external
	// balancer announces the LoadBalancer addresses instead).
	DisableL2Announcements bool
	// BGP turns on Cilium's BGP control plane.
	BGP bool
	// Hubble turns on Hubble with its relay (no UI).
	Hubble bool
	// OperatorReplicas is the operator's replica count. Zero: 2.
	OperatorReplicas int
	// PolicyAuditMode logs what NetworkPolicies would drop instead of
	// dropping it: for introducing policies to a running cluster.
	PolicyAuditMode bool
	// Overrides are merged over the result, key by key at the top level.
	Overrides map[string]any
}

// Values returns the Cilium chart values for the options.
func Values(o Options) (map[string]any, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}

	port := o.KubePrismPort
	if port == 0 {
		port = DefaultKubePrismPort
	}

	replicas := o.OperatorReplicas
	if replicas == 0 {
		replicas = 2
	}

	v := map[string]any{
		// Talos: the API through KubePrism, no kube-proxy.
		"k8sServiceHost":       "localhost",
		"k8sServicePort":       port,
		"kubeProxyReplacement": true,
		"ipam":                 map[string]any{"mode": "kubernetes"},
		// Talos mounts cgroup v2 itself and forbids the agent's mount.
		"cgroup": map[string]any{
			"autoMount": map[string]any{"enabled": false},
			"hostRoot":  "/sys/fs/cgroup",
		},
		// Talos refuses SYS_MODULE: the capability lists without it.
		"securityContext": map[string]any{"capabilities": map[string]any{
			"ciliumAgent": []string{
				"CHOWN", "KILL", "NET_ADMIN", "NET_RAW", "IPC_LOCK", "SYS_ADMIN",
				"SYS_RESOURCE", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID",
			},
			"cleanCiliumState": []string{"NET_ADMIN", "SYS_ADMIN", "SYS_RESOURCE"},
		}},
		"bpf":      map[string]any{"masquerade": true},
		"operator": map[string]any{"replicas": replicas},
		// NetworkPolicy is enforced as Kubernetes defines it: a pod no
		// policy selects is open.
		"policyEnforcementMode": "default",
		"policyAuditMode":       o.PolicyAuditMode,
		"hubble":                map[string]any{"enabled": o.Hubble, "relay": map[string]any{"enabled": o.Hubble}},
	}

	if !o.DisableL2Announcements {
		v["l2announcements"] = map[string]any{"enabled": true}
		v["externalIPs"] = map[string]any{"enabled": true}
		// Every announced Service holds a lease renewed through the API;
		// the default client rate limit starves under a few dozen.
		v["k8sClientRateLimit"] = map[string]any{"qps": 50, "burst": 100}
	}

	if o.BGP {
		v["bgpControlPlane"] = map[string]any{"enabled": true}
	}

	if o.NativeRoutingCIDR != "" {
		v["routingMode"] = "native"
		v["autoDirectNodeRoutes"] = true
		v["ipv4NativeRoutingCIDR"] = o.NativeRoutingCIDR
	}

	if len(o.Devices) > 0 {
		v["devices"] = o.Devices
	}

	maps.Copy(v, o.Overrides)

	return v, nil
}

func (o Options) validate() error {
	var errs []error

	if o.KubePrismPort < 0 || o.KubePrismPort > 65535 {
		errs = append(errs, fmt.Errorf("cilium: KubePrismPort %d is not a port", o.KubePrismPort))
	}

	if o.OperatorReplicas < 0 {
		errs = append(errs, fmt.Errorf("cilium: OperatorReplicas %d is negative", o.OperatorReplicas))
	}

	if o.NativeRoutingCIDR != "" {
		p, err := netip.ParsePrefix(o.NativeRoutingCIDR)
		if err != nil || !p.Addr().Is4() || p.Masked() != p {
			errs = append(errs, fmt.Errorf("cilium: NativeRoutingCIDR %q is not an IPv4 CIDR without host bits", o.NativeRoutingCIDR))
		}
	}

	if o.DisableL2Announcements && !o.BGP {
		errs = append(errs, errors.New("cilium: with L2 announcements off and no BGP, nothing announces LoadBalancer addresses; "+
			"set BGP, or keep L2 on, or set Overrides deliberately with an external balancer"))
	}

	for i, d := range o.Devices {
		if d == "" {
			errs = append(errs, fmt.Errorf("cilium: Devices[%d] is empty", i))
		}
	}

	return errors.Join(errs...)
}
