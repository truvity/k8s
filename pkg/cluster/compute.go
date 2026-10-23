package cluster

import "maps"

type (
	// ComputeInputs are the facts one cluster's compute model is derived from.
	ComputeInputs struct {
		// PresetPools are the pools of the cluster's preset (a profile shared by
		// several clusters); ClusterPools are the cluster's own, and replace a
		// preset pool of the same name. A pool that exists on one cluster only is
		// the cluster's, not the preset's.
		PresetPools  map[string]NodePoolSpec
		ClusterPools map[string]NodePoolSpec
		// Classes are the cluster's EFFECTIVE shared NodeClasses (the caller has
		// already laid the cluster's own over its preset's).
		Classes map[string]NodeClassSpec
		// DefaultClass names the shared class a pool with no class of its own, and
		// none named, gets; empty leaves such a pool on Auto Mode's built-in one.
		DefaultClass string
		// Defaults are what a pool gets for the choices it does not state.
		Defaults PoolDefaults
		// NodeRoleArn is the cluster's Auto Mode node role, reported by the
		// provider; needed by every NodeClass, so by every pool that has its own.
		NodeRoleArn string
	}

	// Compute is the compute model of one cluster, as the eks-auto-node-pools
	// chart takes it: nodePools and nodeClasses.
	Compute struct {
		NodePools   []NodePool
		NodeClasses []NodeClass
	}
)

// DeriveCompute builds a cluster's node pools and shared NodeClasses from its
// preset's pools with the cluster's own on top (same name replaces), the
// defaults and the cluster's default class.
//
// A cluster with no pool at all gets the zero Compute and no error: whether
// that is acceptable (the provisioner may require pools) is the caller's
// decision, and a NodeClass is only derived beside pools that use one.
func DeriveCompute(in ComputeInputs) (Compute, error) {
	pools := map[string]NodePoolSpec{}
	maps.Copy(pools, in.PresetPools)
	maps.Copy(pools, in.ClusterPools)

	if len(pools) == 0 {
		return Compute{}, nil
	}

	nodePools, err := DeriveNodePools(pools, in.Defaults, in.NodeRoleArn)
	if err != nil {
		return Compute{}, err
	}

	ApplyDefaultNodeClass(nodePools, in.DefaultClass)

	nodeClasses, err := DeriveNodeClasses(in.Classes, in.NodeRoleArn)
	if err != nil {
		return Compute{}, err
	}

	return Compute{NodePools: nodePools, NodeClasses: nodeClasses}, nil
}
