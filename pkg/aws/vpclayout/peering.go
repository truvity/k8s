package vpclayout

import (
	"fmt"
	"net"
	"sort"
)

type (
	// Peering is a hub-and-spoke peering of a Plan's networks: every spoke is
	// peered with the hub and with nothing else, so a spoke reaches another
	// spoke only through the hub.
	Peering struct {
		// Hub is the network every spoke is peered with.
		Hub string
		// Connections are the hub's peerings, one per spoke.
		Connections []Connection
	}

	// Connection is the peering of the hub with one spoke.
	Connection struct {
		// Name identifies the connection. Required, unique.
		Name string
		// Spoke is the peered network. Required, one connection each.
		Spoke string
	}
)

// ValidateIndexes refuses two networks with the same index, which the Plan
// gives the same /16. networks maps a network's name to its index.
func ValidateIndexes(networks map[string]int) error {
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}

	sort.Strings(names)

	byIndex := make(map[int]string, len(names))

	for _, name := range names {
		idx := networks[name]
		if other, ok := byIndex[idx]; ok {
			return fmt.Errorf("networks %q and %q share network_index %d, so the same CIDR", other, name, idx)
		}

		byIndex[idx] = name
	}

	return nil
}

// Validate holds the peering to what AWS needs: a hub and spokes that are
// networks (the keys of networks), unique connection names, one connection
// per spoke and none of the hub with itself. A peering with no connection is
// valid whatever its hub.
func (p Peering) Validate(networks map[string]int) error {
	if len(p.Connections) == 0 {
		return nil
	}

	if _, ok := networks[p.Hub]; !ok {
		return fmt.Errorf("vpc_peering.hub %q is not a physical network", p.Hub)
	}

	names, spokes := map[string]bool{}, map[string]bool{}

	for _, conn := range p.Connections {
		if conn.Name == "" || names[conn.Name] {
			return fmt.Errorf("vpc_peering connection name %q is empty or duplicate", conn.Name)
		}

		names[conn.Name] = true

		if spokes[conn.Spoke] {
			return fmt.Errorf("vpc_peering spoke %q has two connections", conn.Spoke)
		}

		spokes[conn.Spoke] = true

		if conn.Spoke == p.Hub {
			return fmt.Errorf("vpc_peering connection %q peers the hub with itself", conn.Name)
		}

		if _, ok := networks[conn.Spoke]; !ok {
			return fmt.Errorf("vpc_peering connection %q: spoke %q is not a physical network", conn.Name, conn.Spoke)
		}
	}

	return nil
}

// SpokeCIDRsOutside returns the VPC CIDRs of the spokes that supernet does not
// cover, sorted. networks maps a network's name to its index; a spoke with no
// entry is an error. An empty supernet covers nothing, so every spoke's CIDR
// is returned.
//
// Whatever admits "the peered VPCs" by the supernet (a network ACL rule, a
// load balancer's source ranges, a NetworkPolicy) must admit these too.
func (pl Plan) SpokeCIDRsOutside(supernet string, p Peering, networks map[string]int) ([]string, error) {
	var super *net.IPNet

	if supernet != "" {
		_, n, err := net.ParseCIDR(supernet)
		if err != nil {
			return nil, fmt.Errorf("invalid vpc_supernet %q: %w", supernet, err)
		}

		super = n
	}

	var out []string

	for _, conn := range p.Connections {
		idx, ok := networks[conn.Spoke]
		if !ok {
			return nil, fmt.Errorf("spoke %q not found", conn.Spoke)
		}

		cidr := pl.VPCCIDR(idx)

		if super != nil {
			_, n, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, fmt.Errorf("spoke %q CIDR %q: %w", conn.Spoke, cidr, err)
			}

			if super.Contains(n.IP) {
				continue
			}
		}

		out = append(out, cidr)
	}

	sort.Strings(out)

	return out, nil
}

// RouterlessSpokes returns the spokes that run no router of their own, sorted:
// the networks the hub's routers reach over the peering. networks maps a
// network's name to its index, and a spoke with no entry is left out;
// hasRouter reports whether a network runs a router.
func (p Peering) RouterlessSpokes(networks map[string]int, hasRouter func(string) bool) []string {
	var out []string

	for _, conn := range p.Connections {
		if _, ok := networks[conn.Spoke]; ok && !hasRouter(conn.Spoke) {
			out = append(out, conn.Spoke)
		}
	}

	sort.Strings(out)

	return out
}

// RouterlessSpokeCIDRs returns the VPC CIDRs of the routerless spokes peered
// to network, sorted. Only the hub has any: a spoke's one peer is the hub.
// The hub's routers advertise these routes next to the hub's own CIDR, so a
// peered VPC with no router is reached through the hub's, over the peering;
// a spoke with a router is reached through that router and is left out.
// networks maps a network's name to its index.
func (pl Plan) RouterlessSpokeCIDRs(network string, p Peering, networks map[string]int, hasRouter func(string) bool) []string {
	if network != p.Hub {
		return nil
	}

	var out []string

	for _, conn := range p.Connections {
		if idx, ok := networks[conn.Spoke]; ok && !hasRouter(conn.Spoke) {
			out = append(out, pl.VPCCIDR(idx))
		}
	}

	sort.Strings(out)

	return out
}
