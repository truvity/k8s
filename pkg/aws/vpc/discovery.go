package vpc

import "strings"

// The tag keys Kubernetes and the AWS load balancer controllers discover
// subnets by.
const (
	// TagClusterPrefix followed by a cluster name marks a subnet as the
	// cluster's. The value "shared" says the subnet is not owned by the
	// cluster's lifecycle: this component owns it.
	TagClusterPrefix = "kubernetes.io/cluster/"
	// TagRoleELB marks a public subnet for internet-facing load balancers.
	TagRoleELB = "kubernetes.io/role/elb"
	// TagRoleInternalELB marks a subnet for internal load balancers.
	TagRoleInternalELB = "kubernetes.io/role/internal-elb"
)

// DiscoveryTags are the tags that let a cluster and its load balancer
// controller discover a subnet, for Subnet.Tags: the cluster tag on every
// subnet, the elb role on a public one, and the internal-elb role on the
// subnets that internalELB is true for (the caller picks which private subnets
// host internal load balancers). An empty cluster, a VPC that holds no
// cluster, gets none.
func DiscoveryTags(cluster string, subnetType SubnetType, internalELB bool) map[string]string {
	if strings.TrimSpace(cluster) == "" {
		return nil
	}

	tags := map[string]string{TagClusterPrefix + cluster: "shared"}

	if subnetType == Public {
		tags[TagRoleELB] = "1"
	}

	if internalELB {
		tags[TagRoleInternalELB] = "1"
	}

	return tags
}
