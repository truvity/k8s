// Package autolb finds a load balancer that EKS Auto Mode provisioned for a
// Service, by the tags Auto Mode puts on it.
//
// There is nothing to look up by name: Auto Mode generates the load
// balancer's name, so the Service that asked for it is only recoverable from
// the TagServiceStack tag (`<namespace>/<service>`), and the cluster from
// TagClusterName. Both must match: the same namespace/service pair can exist
// on more than one cluster in an account.
//
// It is a read through the AWS SDK, not a Pulumi resource: a program that
// publishes the load balancer's DNS name (a private record, say) looks it up
// before it declares the record.
package autolb

import (
	"context"
	"fmt"

	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

// Tags EKS Auto Mode puts on a load balancer it provisions for a Service.
const (
	// TagServiceStack carries "<namespace>/<service>" of the Service.
	TagServiceStack = "service.eks.amazonaws.com/stack"
	// TagClusterName carries the cluster's name.
	TagClusterName = "eks:eks-cluster-name"

	// describeTagsMax is how many ARNs DescribeTags takes per call.
	describeTagsMax = 20
)

// API is the ELBv2 surface this package needs, narrow so callers can fake it.
// The AWS SDK client (elasticloadbalancingv2.Client) satisfies it.
type API interface {
	DescribeLoadBalancers(
		ctx context.Context, in *elbv2.DescribeLoadBalancersInput, optFns ...func(*elbv2.Options),
	) (*elbv2.DescribeLoadBalancersOutput, error)
	DescribeTags(ctx context.Context, in *elbv2.DescribeTagsInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTagsOutput, error)
}

// FindAutoModeNLB returns the DNS name of the load balancer Auto Mode made for
// stackTag ("<namespace>/<service>") on cluster.
//
// The three outcomes all mean something, and none is a panic:
//
//   - "" with no error: it does not exist yet, because whatever creates the
//     Service has not run. The caller is expected to warn and skip, so a
//     re-run completes the work.
//   - one match: its DNS name.
//   - more than one: an error. Two load balancers claiming one Service on one
//     cluster means something is wrong that a guess would hide.
//
// An API error is always an error, never a silent skip: "not found" and
// "could not look" must not be the same answer.
func FindAutoModeNLB(ctx context.Context, lbs API, cluster, stackTag string) (string, error) {
	dnsByARN := map[string]string{}

	var arns []string

	pages := elbv2.NewDescribeLoadBalancersPaginator(lbs, &elbv2.DescribeLoadBalancersInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("describe load balancers: %w", err)
		}

		for i := range page.LoadBalancers {
			lb := &page.LoadBalancers[i]
			if lb.LoadBalancerArn == nil || lb.DNSName == nil {
				continue
			}

			arns = append(arns, *lb.LoadBalancerArn)
			dnsByARN[*lb.LoadBalancerArn] = *lb.DNSName
		}
	}

	var found []string

	for start := 0; start < len(arns); start += describeTagsMax {
		end := min(start+describeTagsMax, len(arns))

		out, err := lbs.DescribeTags(ctx, &elbv2.DescribeTagsInput{ResourceArns: arns[start:end]})
		if err != nil {
			return "", fmt.Errorf("describe load balancer tags: %w", err)
		}

		for _, desc := range out.TagDescriptions {
			if desc.ResourceArn != nil && hasTags(desc.Tags, cluster, stackTag) {
				found = append(found, dnsByARN[*desc.ResourceArn])
			}
		}
	}

	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("%d load balancers tagged %s=%s on %s; expected one", len(found), TagServiceStack, stackTag, cluster)
	}
}

func hasTags(tags []elbv2types.Tag, cluster, stackTag string) bool {
	var stack, owner bool

	for _, t := range tags {
		if t.Key == nil || t.Value == nil {
			continue
		}

		switch *t.Key {
		case TagServiceStack:
			stack = *t.Value == stackTag
		case TagClusterName:
			owner = *t.Value == cluster
		}
	}

	return stack && owner
}
