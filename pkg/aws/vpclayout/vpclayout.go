// Package vpclayout is the address plan of a fleet of VPCs: which /16 a
// network gets, how its logical subnets are cut into one block per
// availability zone, and what each block is called.
//
// It is arithmetic over declared facts and has no cloud dependency. A
// consumer declares a Plan (the second octet of network 0), its networks
// (an index, a region, the logical subnets with their third octet and prefix
// length) and the availability zones with their slot index, and gets back CIDR
// strings that pkg/aws/vpc takes as Args.
//
// Addressing rules: network P is 10.(Base+P).0.0/16. A logical subnet starts at
// its third octet. Unless it sets a prefix length, each AZ's block is a /24
// and a multi-AZ network's logical block is the /22 over four /24 slots; a
// subnet with a larger block (prefix length 16 to 23) places AZ i at
// third_octet + i*2^(24-prefix) and its logical label is the first AZ's block.
package vpclayout

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

type (
	// Plan is the fleet's addressing: network P is 10.(Base+P).0.0/16.
	Plan struct {
		// Base is the second octet of network 0.
		Base int
	}

	// Subnet is one logical subnet of a network.
	Subnet struct {
		// ThirdOctet is the first AZ's block base.
		ThirdOctet int
		// Type is "public" or "private"; it is carried to the result.
		Type string
		// PrefixLen is the prefix length of each AZ's block; 0 is 24. A larger
		// block (16 to 23) is for a subnet that hosts pods, which need an
		// address each. ThirdOctet must be a multiple of 2^(24-PrefixLen).
		PrefixLen int
	}

	// Network is one physical network.
	Network struct {
		// Index is the network's number within the Plan.
		Index   int
		Subnets map[string]Subnet
	}

	// AZ is one availability zone with its slot in a /22.
	AZ struct {
		Name  string
		Index int
	}

	// Calculated is the CIDR information of one logical subnet.
	Calculated struct {
		// Name is the logical subnet name.
		Name string
		// CIDR is the logical block: the /22 over the AZ /24s of a multi-AZ
		// network, the /24 of a single-AZ one; for a subnet with a prefix
		// length, the first AZ's block.
		CIDR string
		Type string
		// AZSubnets maps an AZ name to its block.
		AZSubnets map[string]string
	}
)

// VPCCIDR is the network's VPC CIDR: 10.(Base+Index).0.0/16.
func (p Plan) VPCCIDR(index int) string {
	return fmt.Sprintf("10.%d.0.0/16", p.Base+index)
}

// DNSIP is the VPC's resolver address (the VPC base + 2).
func (p Plan) DNSIP(index int) string {
	return fmt.Sprintf("10.%d.0.2", p.Base+index)
}

// PublicSubnetCIDR is the /22 logical block of the network's "public" subnet.
// Multi-AZ networks split the /22 into per-AZ /24s, so the block covers every
// AZ's public subnet. The canonical /22 base is (third_octet / 4) * 4.
func (p Plan) PublicSubnetCIDR(n Network) (string, error) {
	public, ok := n.Subnets["public"]
	if !ok {
		return "", fmt.Errorf("public subnet not found in network")
	}

	if public.ThirdOctet < 0 || public.ThirdOctet > 255 {
		return "", fmt.Errorf("public subnet third_octet %d out of range 0-255", public.ThirdOctet)
	}

	base := (public.ThirdOctet / 4) * 4

	return fmt.Sprintf("10.%d.%d.0/22", p.Base+n.Index, base), nil
}

// FilterAZs returns the AZs of a region, sorted by name: the entries of azs
// (name to slot index) that start with region. A network that is not on aws
// and has no entry gets one implicit AZ named after the region.
func FilterAZs(azs map[string]int, region, provider string) []AZ {
	var filtered []AZ

	for name, index := range azs {
		if strings.HasPrefix(name, region) {
			filtered = append(filtered, AZ{Name: name, Index: index})
		}
	}

	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })

	if len(filtered) == 0 && provider != "aws" {
		filtered = append(filtered, AZ{Name: region, Index: 0})
	}

	return filtered
}

// Calculate cuts every logical subnet of the network into one block per AZ,
// in third-octet order, and refuses a layout in which two blocks overlap.
// name is the network's name, for errors; azs is FilterAZs of its region.
func (p Plan) Calculate(name string, n Network, azs []AZ) ([]Calculated, error) {
	if len(azs) == 0 {
		return nil, fmt.Errorf("network %q: no availability zones", name)
	}

	azCount := len(azs)

	_, vpcIPNet, err := net.ParseCIDR(p.VPCCIDR(n.Index))
	if err != nil {
		return nil, fmt.Errorf("parse VPC CIDR %q: %w", p.VPCCIDR(n.Index), err)
	}

	vpcIP := vpcIPNet.IP.To4()

	subnetNames := sortedSubnetNames(n.Subnets)

	result := make([]Calculated, 0, len(subnetNames))

	for _, subnetName := range subnetNames {
		subnet := n.Subnets[subnetName]

		if subnet.ThirdOctet < 0 || subnet.ThirdOctet > 255 {
			return nil, fmt.Errorf("subnet %q: third_octet %d out of range 0-255", subnetName, subnet.ThirdOctet)
		}

		// Per-AZ prefix: /24 unless the subnet asks for a larger block.
		prefixLen := 24
		if subnet.PrefixLen != 0 {
			prefixLen = subnet.PrefixLen
		}

		if prefixLen < 16 || prefixLen > 24 {
			return nil, fmt.Errorf("subnet %q: prefix_len %d out of range 16-24", subnetName, prefixLen)
		}

		// Third-octet steps between consecutive AZs: 1 for /24, 32 for /19.
		stride := 1 << (24 - prefixLen)
		if subnet.ThirdOctet%stride != 0 {
			return nil, fmt.Errorf("subnet %q: third_octet %d must be a multiple of %d for prefix_len /%d",
				subnetName, subnet.ThirdOctet, stride, prefixLen)
		}

		subnetIP := net.IPv4(vpcIP[0], vpcIP[1], byte(subnet.ThirdOctet), 0).To4()

		// Logical CIDR: /22 for multi-AZ (covers 4 x /24), /24 for single-AZ.
		logicalPrefix := 24
		if azCount > 1 {
			logicalPrefix = 22
		}

		if subnet.PrefixLen != 0 {
			// Not a /22 of /24s: the AZ blocks are /prefix_len and the
			// logical label is the first AZ's block.
			logicalPrefix = prefixLen
		}

		// A /22 spans 4 third-octet values, so its base is a multiple of 4.
		canonicalOctet := subnet.ThirdOctet
		if logicalPrefix == 22 && subnet.PrefixLen == 0 {
			canonicalOctet = (subnet.ThirdOctet / 4) * 4
		}

		logicalIP := net.IPv4(vpcIP[0], vpcIP[1], byte(canonicalOctet), 0).To4()
		subnetCIDR := fmt.Sprintf("%s/%d", logicalIP.String(), logicalPrefix)

		azSubnets := make(map[string]string, azCount)

		for _, az := range azs {
			if az.Index < 0 {
				return nil, fmt.Errorf("subnet %q: az %q has negative index %d", subnetName, az.Name, az.Index)
			}

			// For multi-AZ (/22) blocks the index must fit within the block.
			if azCount > 1 && subnet.PrefixLen == 0 {
				slotsPerBlock := 1 << (prefixLen - logicalPrefix) // 4 for /22
				if az.Index >= slotsPerBlock {
					return nil, fmt.Errorf(
						"subnet %q: az %q index %d exceeds /%d block capacity (%d slots)",
						subnetName, az.Name, az.Index, logicalPrefix, slotsPerBlock,
					)
				}
			}

			if subnet.ThirdOctet+az.Index*stride > 255 {
				return nil, fmt.Errorf(
					"subnet %q: az %q index %d + third_octet %d overflows third octet byte",
					subnetName, az.Name, az.Index, subnet.ThirdOctet,
				)
			}

			azIP := make(net.IP, len(subnetIP))
			copy(azIP, subnetIP)

			azIP[2] += byte(az.Index * stride)

			azSubnets[az.Name] = fmt.Sprintf("%s/%d", azIP.String(), prefixLen)
		}

		result = append(result, Calculated{Name: subnetName, CIDR: subnetCIDR, Type: subnet.Type, AZSubnets: azSubnets})
	}

	if err := checkNoOverlap(name, result); err != nil {
		return nil, err
	}

	return result, nil
}

// checkNoOverlap refuses a layout in which two subnets (in any AZ) share
// addresses. With a uniform /24 grid that cannot happen; once a subnet may be
// a /19 it can, and EC2 would only say so at apply time.
func checkNoOverlap(networkName string, subnets []Calculated) error {
	type owned struct {
		name string
		net  *net.IPNet
	}

	var all []owned

	for _, cs := range subnets {
		for az, cidr := range cs.AZSubnets {
			_, n, err := net.ParseCIDR(cidr)
			if err != nil {
				return fmt.Errorf("network %q: subnet %q az %q: %w", networkName, cs.Name, az, err)
			}

			all = append(all, owned{name: cs.Name + "/" + az, net: n})
		}
	}

	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if all[i].net.Contains(all[j].net.IP) || all[j].net.Contains(all[i].net.IP) {
				return fmt.Errorf("network %q: subnets %s (%s) and %s (%s) overlap",
					networkName, all[i].name, all[i].net, all[j].name, all[j].net)
			}
		}
	}

	return nil
}

// SubnetName is a subnet's Name tag: {env}-{subnet}-{az suffix}.
func SubnetName(env, subnet, az string) string {
	return fmt.Sprintf("%s-%s-%s", env, subnet, AZSuffix(az))
}

// AZSuffix is the last character of an AZ name ("region-1a" is "a").
func AZSuffix(az string) string {
	if az == "" {
		return ""
	}

	return string(az[len(az)-1])
}

// sortedSubnetNames returns subnet names sorted by their ThirdOctet value.
func sortedSubnetNames(subnets map[string]Subnet) []string {
	names := make([]string, 0, len(subnets))
	for name := range subnets {
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool { return subnets[names[i]].ThirdOctet < subnets[names[j]].ThirdOctet })

	return names
}
