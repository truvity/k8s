package cluster

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The gp3 baseline of a pool's own NodeClass volume: what Auto Mode's
// `default` NodeClass runs, and what gp3 includes at no extra charge.
const (
	DefaultEphemeralIOPS       = 3000
	DefaultEphemeralThroughput = 125
)

// The taint keys and values the derivation writes.
const (
	archARM64       = "arm64"
	archAMD64       = "amd64"
	taintTrue       = "true"
	taintNoSchedule = "NoSchedule"
	taintArchKey    = "arch"
)

// ephemeralSizeRE admits whole Gi only. The NodeClass takes any quantity; one
// unit keeps the value comparable at a glance.
var ephemeralSizeRE = regexp.MustCompile(`^([1-9]\d*)Gi$`)

type (
	// EphemeralStorage is the gp3 data volume of a NodeClass
	// (eks.amazonaws.com/v1 NodeClass spec.ephemeralStorage). Zero IOPS or
	// Throughput mean the gp3 baseline.
	EphemeralStorage struct {
		// Size in whole Gi, e.g. "200Gi". Required.
		Size       string `yaml:"size"`
		IOPS       int    `yaml:"iops"`
		Throughput int    `yaml:"throughput"`
	}

	// NodePoolSpec is one pool as a consumer declares it: every field is the
	// consumer's choice, and the unset ones take the PoolDefaults.
	NodePoolSpec struct {
		Archs         []string
		CapacityTypes []string
		// Categories are the instance categories (eks.amazonaws.com/instance-category): c, m, r or t.
		Categories []string
		// Dedicated repels every pod that does not tolerate the {name}=true taint.
		Dedicated bool
		// Weight prefers this pool when several match (higher wins).
		Weight int
		// CPULimit caps the pool's total vCPUs.
		CPULimit int
		// MaxInstanceCPU caps the vCPUs of a SINGLE node (inclusive); 0 is unset.
		MaxInstanceCPU         int
		TerminationGracePeriod string
		ExpireAfter            string
		ConsolidationPolicy    string
		FastEmptyReclaim       bool
		// Zones pins the pool to these AZs; empty is all.
		Zones []string
		// EphemeralStorage, when set, gives the pool a NodeClass of its own
		// named after the pool.
		EphemeralStorage *EphemeralStorage
		// PodSubnets takes pod IPs from the pods subnets (needs EphemeralStorage).
		PodSubnets bool
		// NodeClass names a SHARED NodeClass; excludes EphemeralStorage and PodSubnets.
		NodeClass string
	}

	// NodeClassSpec is a shared NodeClass as a consumer declares it.
	NodeClassSpec struct {
		EphemeralStorage EphemeralStorage
	}

	// PoolDefaults are the choices a pool gets when it makes none. They are
	// the consumer's policy, not the library's: the chart requires every pool
	// to state them.
	PoolDefaults struct {
		Archs         []string
		CapacityTypes []string
		Categories    []string
	}

	// Taint is a Kubernetes taint on a pool's nodes.
	Taint struct {
		Key    string `yaml:"key"`
		Value  string `yaml:"value,omitempty"`
		Effect string `yaml:"effect"`
	}

	// NodePool is one entry of the eks-auto-node-pools chart's nodePools.
	NodePool struct {
		Name                   string   `yaml:"name"`
		Weight                 int      `yaml:"weight,omitempty"`
		Archs                  []string `yaml:"archs"`
		CapacityTypes          []string `yaml:"capacityTypes"`
		Categories             []string `yaml:"categories"`
		Taints                 []Taint  `yaml:"taints,omitempty"`
		CPULimit               int      `yaml:"cpuLimit,omitempty"`
		MaxInstanceCPU         int      `yaml:"maxInstanceCpu,omitempty"`
		ExpireAfter            string   `yaml:"expireAfter,omitempty"`
		TerminationGracePeriod string   `yaml:"terminationGracePeriod,omitempty"`
		ConsolidationPolicy    string   `yaml:"consolidationPolicy,omitempty"`
		FastEmptyReclaim       bool     `yaml:"fastEmptyReclaim,omitempty"`
		Zones                  []string `yaml:"zones,omitempty"`
		// NodeClass is the pool's OWN NodeClass; nil keeps the pool on a
		// shared one or on Auto Mode's built-in `default`.
		NodeClass *NodeClass `yaml:"nodeClass,omitempty"`
		// NodeClassName is a SHARED NodeClass.
		NodeClassName string `yaml:"nodeClassName,omitempty"`
	}

	// NodeClass is one entry of the chart's nodeClasses, or a pool's own.
	NodeClass struct {
		Name string `yaml:"name"`
		// Role is the node IAM role NAME: the cluster's Auto Mode node role,
		// whose EC2 access entry is what admits the node.
		Role             string           `yaml:"role"`
		EphemeralStorage EphemeralStorage `yaml:"ephemeralStorage"`
		PodSubnets       bool             `yaml:"podSubnets,omitempty"`
	}
)

// DeriveNodePools builds the chart's nodePools from the declared pools,
// sorted by name. nodeRoleArn is the cluster's Auto Mode node role; only a
// pool with its own NodeClass needs it, and such a pool is refused without it.
func DeriveNodePools(pools map[string]NodePoolSpec, defaults PoolDefaults, nodeRoleArn string) ([]NodePool, error) {
	names := make([]string, 0, len(pools))
	for name := range pools {
		names = append(names, name)
	}

	sort.Strings(names)

	out := make([]NodePool, 0, len(names))

	for _, name := range names {
		np := pools[name]

		archs := np.Archs
		if len(archs) == 0 {
			archs = defaults.Archs
		}

		capacityTypes := np.CapacityTypes
		if len(capacityTypes) == 0 {
			capacityTypes = defaults.CapacityTypes
		}

		categories := np.Categories
		if len(categories) == 0 {
			categories = defaults.Categories
		}

		// No custom labels: kubelet REJECTS self-set node-role.kubernetes.io
		// labels. Selectors use the karpenter.sh/nodepool={name} label
		// Karpenter sets automatically.
		var taints []Taint

		// Dedicated pools repel everything that does not tolerate them.
		if np.Dedicated {
			taints = append(taints, Taint{Key: name, Value: taintTrue, Effect: taintNoSchedule})
		}

		// arm64-only pools get the arch taint to keep arch-blind images off.
		if len(archs) == 1 && archs[0] == archARM64 {
			taints = append(taints, Taint{Key: taintArchKey, Value: archARM64, Effect: taintNoSchedule})
		}

		pool := NodePool{
			Name:                   name,
			Weight:                 np.Weight,
			Archs:                  archs,
			CapacityTypes:          capacityTypes,
			Categories:             categories,
			Taints:                 taints,
			CPULimit:               np.CPULimit,
			MaxInstanceCPU:         np.MaxInstanceCPU,
			ExpireAfter:            np.ExpireAfter,
			TerminationGracePeriod: np.TerminationGracePeriod,
			ConsolidationPolicy:    np.ConsolidationPolicy,
			FastEmptyReclaim:       np.FastEmptyReclaim,
			Zones:                  np.Zones,
			NodeClassName:          np.NodeClass,
		}

		if np.EphemeralStorage != nil {
			nodeClass, err := deriveNodeClass(name, np.EphemeralStorage, nodeRoleArn)
			if err != nil {
				return nil, err
			}

			nodeClass.PodSubnets = np.PodSubnets
			pool.NodeClass = nodeClass
		}

		out = append(out, pool)
	}

	return out, nil
}

// ApplyDefaultNodeClass names def, the cluster's default class, on every pool
// that has neither a NodeClass of its own nor an explicit NodeClassName.
func ApplyDefaultNodeClass(pools []NodePool, def string) {
	if def == "" {
		return
	}

	for i := range pools {
		if pools[i].NodeClass == nil && pools[i].NodeClassName == "" {
			pools[i].NodeClassName = def
		}
	}
}

// DeriveNodeClasses is the shared NodeClasses, sorted by name: each on the
// cluster's node role with its own volume.
func DeriveNodeClasses(classes map[string]NodeClassSpec, nodeRoleArn string) ([]NodeClass, error) {
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}

	sort.Strings(names)

	out := make([]NodeClass, 0, len(names))

	for _, name := range names {
		es := classes[name].EphemeralStorage

		nc, err := deriveNodeClass(name, &es, nodeRoleArn)
		if err != nil {
			return nil, err
		}

		out = append(out, *nc)
	}

	return out, nil
}

// deriveNodeClass is a NodeClass named after its pool (or its shared name), on
// the cluster's node role, with the volume asked for and gp3's baseline
// wherever it does not say.
func deriveNodeClass(name string, es *EphemeralStorage, nodeRoleArn string) (*NodeClass, error) {
	// arn:aws:iam::<acct>:role[/<path>]/<name> -- the NodeClass takes the
	// NAME. An empty or path-less value is a cluster whose node role has not
	// been reported yet: a NodeClass with no role would never turn Ready,
	// and its pool would never provision.
	_, roleName, ok := strings.Cut(nodeRoleArn, ":role/")
	if i := strings.LastIndex(roleName, "/"); i >= 0 {
		roleName = roleName[i+1:]
	}

	if !ok || roleName == "" {
		return nil, fmt.Errorf(
			"node pool %q has ephemeral_storage (its own NodeClass) but there is no autoNodeRoleArn to put in it (got %q)",
			name, nodeRoleArn,
		)
	}

	iops := es.IOPS
	if iops == 0 {
		iops = DefaultEphemeralIOPS
	}

	throughput := es.Throughput
	if throughput == 0 {
		throughput = DefaultEphemeralThroughput
	}

	return &NodeClass{
		Name:             name,
		Role:             roleName,
		EphemeralStorage: EphemeralStorage{Size: es.Size, IOPS: iops, Throughput: throughput},
	}, nil
}

// ValidateNodePool checks one declared pool: its enumerated fields, its
// limits, its zones, its volume and its NodeClass reference against classes
// (the shared NodeClasses it may name). scope prefixes every error ("preset
// \"x\"", "cluster y").
func ValidateNodePool(scope, poolName string, np *NodePoolSpec, classes map[string]NodeClassSpec) error {
	for _, arch := range np.Archs {
		if arch != archARM64 && arch != archAMD64 {
			return fmt.Errorf("%s: node_pools.%s.archs contains %q (must be arm64 or amd64)", scope, poolName, arch)
		}
	}

	for _, ct := range np.CapacityTypes {
		if ct != "spot" && ct != "on-demand" {
			return fmt.Errorf("%s: node_pools.%s.capacity_types contains %q (must be spot or on-demand)", scope, poolName, ct)
		}
	}

	for _, cat := range np.Categories {
		if cat != "c" && cat != "m" && cat != "r" && cat != "t" {
			return fmt.Errorf("%s: node_pools.%s.categories contains %q (must be c, m, r or t)", scope, poolName, cat)
		}
	}

	if np.CPULimit < 0 {
		return fmt.Errorf("%s: node_pools.%s.cpu_limit must be >= 0, got %d", scope, poolName, np.CPULimit)
	}

	// EKS Auto Mode refuses anything at or below 1 vCPU, so a ceiling under 2
	// admits no instance at all: a pool that can never provision, which fails
	// at 3am rather than at render.
	if np.MaxInstanceCPU < 0 || np.MaxInstanceCPU == 1 {
		return fmt.Errorf("%s: node_pools.%s.max_instance_cpu must be 0 (unset) or >= 2, got %d", scope, poolName, np.MaxInstanceCPU)
	}

	if np.CPULimit > 0 && np.MaxInstanceCPU > np.CPULimit {
		return fmt.Errorf(
			"%s: node_pools.%s.max_instance_cpu (%d) exceeds cpu_limit (%d) — one node could not fit inside the pool",
			scope, poolName, np.MaxInstanceCPU, np.CPULimit,
		)
	}

	if err := ValidateEphemeralStorage(scope, poolName, np.EphemeralStorage); err != nil {
		return err
	}

	if err := validateNodeClassRef(scope, poolName, np, classes); err != nil {
		return err
	}

	if np.PodSubnets && np.EphemeralStorage == nil {
		return fmt.Errorf("%s: node_pools.%s.pod_subnets needs ephemeral_storage (the pool's own NodeClass): "+
			"`default` is Auto Mode's and the chart never renders it", scope, poolName)
	}

	return validateZoneList(scope, poolName, np.Zones)
}

// ValidateNodeClass checks a shared NodeClass's volume. A shared class is
// named like a pool's own: `default` is Auto Mode's, and its volume obeys the
// same NodeClass ranges.
func ValidateNodeClass(scope, className string, nc *NodeClassSpec) error {
	return ValidateEphemeralStorage(fmt.Sprintf("%s: node_classes.%s", scope, className), className, &nc.EphemeralStorage)
}

// validateZoneList checks a pool's zones in isolation: no blanks, no duplicates.
func validateZoneList(scope, poolName string, zones []string) error {
	seen := make(map[string]bool, len(zones))

	for _, zone := range zones {
		if zone == "" {
			return fmt.Errorf("%s: node_pools.%s.zones contains an empty entry", scope, poolName)
		}

		if seen[zone] {
			return fmt.Errorf("%s: node_pools.%s.zones lists %q twice", scope, poolName, zone)
		}

		seen[zone] = true
	}

	return nil
}

// validateNodeClassRef checks a pool's NodeClass against the shared classes: it
// must exist, and it excludes the pool's own NodeClass.
func validateNodeClassRef(scope, poolName string, np *NodePoolSpec, classes map[string]NodeClassSpec) error {
	if np.NodeClass == "" {
		return nil
	}

	if _, ok := classes[np.NodeClass]; !ok {
		return fmt.Errorf("%s: node_pools.%s.node_class %q is not in the preset's node_classes", scope, poolName, np.NodeClass)
	}

	if np.EphemeralStorage != nil || np.PodSubnets {
		return fmt.Errorf("%s: node_pools.%s.node_class excludes ephemeral_storage and pod_subnets (a pool has one NodeClass)", scope, poolName)
	}

	return nil
}

// ValidateEphemeralStorage checks a volume against the ranges the
// eks.amazonaws.com/v1 NodeClass admits (size 1-59000Gi, iops 3000-16000,
// throughput 125-1000) and gp3's own ratio (throughput at most IOPS/4), so a
// bad value fails at render instead of as a NodeClass that never turns Ready.
func ValidateEphemeralStorage(scope, poolName string, es *EphemeralStorage) error {
	if es == nil {
		return nil
	}

	// The NodeClass is named after the pool, and `default` is Auto Mode's
	// own: rendering one would fight the service for it.
	if poolName == "default" {
		return fmt.Errorf("%s: node_pools.%s.ephemeral_storage: a pool named \"default\" cannot have its own NodeClass "+
			"(the name is Auto Mode's built-in)", scope, poolName)
	}

	m := ephemeralSizeRE.FindStringSubmatch(es.Size)
	if m == nil {
		return fmt.Errorf("%s: node_pools.%s.ephemeral_storage.size must be whole Gi like \"200Gi\", got %q", scope, poolName, es.Size)
	}

	if gi, err := strconv.Atoi(m[1]); err != nil || gi > 59000 {
		return fmt.Errorf("%s: node_pools.%s.ephemeral_storage.size %s exceeds the NodeClass maximum 59000Gi", scope, poolName, es.Size)
	}

	iops := es.IOPS
	if iops == 0 {
		iops = DefaultEphemeralIOPS
	}

	if iops < DefaultEphemeralIOPS || iops > 16000 {
		return fmt.Errorf("%s: node_pools.%s.ephemeral_storage.iops must be 3000-16000, got %d", scope, poolName, es.IOPS)
	}

	throughput := es.Throughput
	if throughput == 0 {
		throughput = DefaultEphemeralThroughput
	}

	if throughput < DefaultEphemeralThroughput || throughput > 1000 {
		return fmt.Errorf("%s: node_pools.%s.ephemeral_storage.throughput must be 125-1000, got %d", scope, poolName, es.Throughput)
	}

	if throughput*4 > iops {
		return fmt.Errorf("%s: node_pools.%s.ephemeral_storage.throughput %d MiB/s needs at least %d iops (gp3 allows 0.25 MiB/s per IOPS), got %d",
			scope, poolName, throughput, throughput*4, iops)
	}

	return nil
}
