package vpclayout

import (
	"fmt"
)

// HTTPSIngressCIDRs are the sources a network's default network ACL admits on
// a service port from outside the VPC: the supernet (the peered VPCs of the
// fleet) for a network with private subnets; for the hub, also the peered
// VPCs the supernet does not cover (SpokeCIDRsOutside), because the hub's
// private entry is what they reach. A network with no private subnet serves
// nothing and gets none. networks maps a network's name to its index.
//
// The supernet is returned as given, so an empty supernet yields one empty
// entry: validate it before.
func (pl Plan) HTTPSIngressCIDRs(network, supernet string, p Peering, networks map[string]int, hasPrivate bool) ([]string, error) {
	if !hasPrivate {
		return nil, nil
	}

	out := []string{supernet}

	if network == p.Hub {
		extra, err := pl.SpokeCIDRsOutside(supernet, p, networks)
		if err != nil {
			return nil, err
		}

		out = append(out, extra...)
	}

	return out, nil
}

// HubEgressCIDRs are the peered VPCs the hub's default network ACL must let
// extra TCP ports out to: the spokes the supernet does not cover, since
// the broad rules already cover the rest. Only a hub with private subnets has
// any. The ports are the caller's.
func (pl Plan) HubEgressCIDRs(network, supernet string, p Peering, networks map[string]int, hasPrivate bool) ([]string, error) {
	if !hasPrivate || network != p.Hub {
		return nil, nil
	}

	return pl.SpokeCIDRsOutside(supernet, p, networks)
}

// HubSources are the hub's address blocks a routerless spoke admits traffic
// from.
type HubSources struct {
	// PublicBlock is the narrowest CIDR covering every hub public subnet:
	// where the hub's subnet routers sit and source-NAT.
	PublicBlock string
	// NodeCIDRs are the hub's node subnets (the logical blocks whose name
	// starts with a node prefix): the source address of a hub pod's traffic
	// after source NAT.
	NodeCIDRs []string
}

// RouterlessSpokeIngress derives, for a routerless spoke, the hub address
// blocks it admits. It returns false for the hub itself and for any network
// that is not a routerless spoke of p. hub is the hub's Network and
// hubSubnets its Calculate result; nodePrefixes name the node subnets, and
// the hub must have at least one.
func (pl Plan) RouterlessSpokeIngress(
	network string, p Peering, networks map[string]int, hasRouter func(string) bool,
	hub Network, hubSubnets []Calculated, nodePrefixes ...string,
) (HubSources, bool, error) {
	if network == p.Hub {
		return HubSources{}, false, nil
	}

	routerless := false

	for _, s := range p.RouterlessSpokes(networks, hasRouter) {
		if s == network {
			routerless = true
		}
	}

	if !routerless {
		return HubSources{}, false, nil
	}

	block, err := pl.PublicSubnetCIDR(hub)
	if err != nil {
		return HubSources{}, false, fmt.Errorf("hub %q public subnet block: %w", p.Hub, err)
	}

	nodes := LogicalCIDRs(hubSubnets, nodePrefixes...)
	if len(nodes) == 0 {
		return HubSources{}, false, fmt.Errorf("hub %q has no node subnet (prefixes %v)", p.Hub, nodePrefixes)
	}

	return HubSources{PublicBlock: block, NodeCIDRs: nodes}, true, nil
}
