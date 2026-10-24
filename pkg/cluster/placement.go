package cluster

import (
	"fmt"
	"slices"
)

// ValidatePoolZones checks that every zone a pool is pinned to is one of the
// availability zones of the network the cluster runs on. A zone outside the
// network is a pool that can never provision: the NodeClass has no subnet
// there, and Karpenter reports nothing louder than a pending pod. A pool with
// no zones is on all of them. scope prefixes the error (for example
// `cluster "x"`).
func ValidatePoolZones(scope string, pools map[string]NodePoolSpec, azs []string) error {
	for _, name := range sortedKeys(pools) {
		for _, zone := range pools[name].Zones {
			if !slices.Contains(azs, zone) {
				return fmt.Errorf("%s: node_pools.%s.zones names %q, which is not an AZ of the network (%v)",
					scope, name, zone, azs)
			}
		}
	}

	return nil
}

// ValidatePoolPodSubnets checks that a network with no pod subnets hosts no
// pool and no shared class that asks for them: such a NodeClass selects
// nothing, and the pools on it provision no node. hasPodSubnets says whether
// the cluster's network has the pod subnets.
func ValidatePoolPodSubnets(scope string, pools map[string]NodePoolSpec, classes map[string]NodeClassSpec, hasPodSubnets bool) error {
	if hasPodSubnets {
		return nil
	}

	for _, name := range sortedKeys(pools) {
		if pools[name].PodSubnets {
			return fmt.Errorf("%s: node_pools.%s.pod_subnets, but the network has no pod subnets", scope, name)
		}
	}

	for _, name := range sortedKeys(classes) {
		if classes[name].PodSubnets {
			return fmt.Errorf("%s: node_classes.%s.pod_subnets, but the network has no pod subnets", scope, name)
		}
	}

	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
