package autolb_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/truvity/k8s/pkg/aws/autolb"
)

type fakeLBs struct {
	lbs  []elbv2types.LoadBalancer
	tags map[string][]elbv2types.Tag
	err  error
	// tagCalls counts DescribeTags calls; maxARNs is the most ARNs one took.
	tagCalls, maxARNs *int
}

func (f fakeLBs) DescribeLoadBalancers(
	context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options),
) (*elbv2.DescribeLoadBalancersOutput, error) {
	if f.err != nil {
		return nil, f.err
	}

	return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: f.lbs}, nil
}

func (f fakeLBs) DescribeTags(_ context.Context, in *elbv2.DescribeTagsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTagsOutput, error) {
	if f.tagCalls != nil {
		*f.tagCalls++
		*f.maxARNs = max(*f.maxARNs, len(in.ResourceArns))
	}

	out := &elbv2.DescribeTagsOutput{}
	for _, arn := range in.ResourceArns {
		out.TagDescriptions = append(out.TagDescriptions, elbv2types.TagDescription{ResourceArn: aws.String(arn), Tags: f.tags[arn]})
	}

	return out, nil
}

func lb(arn, dns string) elbv2types.LoadBalancer {
	return elbv2types.LoadBalancer{LoadBalancerArn: aws.String(arn), DNSName: aws.String(dns)}
}

func tags(kv ...string) []elbv2types.Tag {
	var out []elbv2types.Tag
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, elbv2types.Tag{Key: aws.String(kv[i]), Value: aws.String(kv[i+1])})
	}

	return out
}

// The four outcomes: absent means "not yet" and is not an error, another
// cluster's load balancer does not match, two matches fail rather than pick,
// and an API error never reads as "not yet".
func TestFindAutoModeNLB(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	const (
		cluster  = "one"
		stackTag = "vault/vault-ui"
	)

	ours := tags(autolb.TagServiceStack, stackTag, autolb.TagClusterName, cluster)

	dnsName, err := autolb.FindAutoModeNLB(ctx, fakeLBs{}, cluster, stackTag)
	if err != nil || dnsName != "" {
		t.Fatalf("no load balancer yet must mean not yet: dns=%q err=%v", dnsName, err)
	}

	dnsName, err = autolb.FindAutoModeNLB(ctx, fakeLBs{
		lbs: []elbv2types.LoadBalancer{lb("arn:other", "other.elb"), lb("arn:ours", "ours.elb")},
		tags: map[string][]elbv2types.Tag{
			"arn:other": tags(autolb.TagServiceStack, "gateway/internal", autolb.TagClusterName, cluster),
			"arn:ours":  ours,
		},
	}, cluster, stackTag)
	if err != nil || dnsName != "ours.elb" {
		t.Fatalf("found by tag: dns=%q err=%v", dnsName, err)
	}

	dnsName, err = autolb.FindAutoModeNLB(ctx, fakeLBs{
		lbs:  []elbv2types.LoadBalancer{lb("arn:ours", "ours.elb")},
		tags: map[string][]elbv2types.Tag{"arn:ours": tags(autolb.TagServiceStack, stackTag, autolb.TagClusterName, "two")},
	}, cluster, stackTag)
	if err != nil || dnsName != "" {
		t.Fatalf("another cluster's load balancer must not match: dns=%q err=%v", dnsName, err)
	}

	if _, err := autolb.FindAutoModeNLB(ctx, fakeLBs{
		lbs:  []elbv2types.LoadBalancer{lb("arn:a", "a.elb"), lb("arn:b", "b.elb")},
		tags: map[string][]elbv2types.Tag{"arn:a": ours, "arn:b": ours},
	}, cluster, stackTag); err == nil {
		t.Fatal("two matching load balancers must fail, not pick one")
	}

	if _, err := autolb.FindAutoModeNLB(ctx, fakeLBs{err: errors.New("throttled")}, cluster, stackTag); err == nil {
		t.Fatal("an API error must fail the run, not silently skip")
	}
}

// The same namespace/service pair can exist on more than one cluster of an
// account: the cluster tag decides.
func TestFindAutoModeNLBDistinguishesClustersAndServices(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	fake := fakeLBs{
		lbs: []elbv2types.LoadBalancer{lb("arn:a", "a.elb"), lb("arn:b", "b.elb")},
		tags: map[string][]elbv2types.Tag{
			"arn:a": tags(autolb.TagServiceStack, "vault/vault-ui", autolb.TagClusterName, "one"),
			"arn:b": tags(autolb.TagServiceStack, "idp/idp-admin", autolb.TagClusterName, "two"),
		},
	}

	for _, tc := range []struct{ cluster, stack, want string }{
		{"two", "idp/idp-admin", "b.elb"},
		{"one", "vault/vault-ui", "a.elb"},
		{"one", "idp/idp-admin", ""},
	} {
		got, err := autolb.FindAutoModeNLB(ctx, fake, tc.cluster, tc.stack)
		if err != nil || got != tc.want {
			t.Errorf("%s on %s: dns=%q err=%v, want %q", tc.stack, tc.cluster, got, err, tc.want)
		}
	}
}

// DescribeTags takes at most twenty ARNs: more load balancers than that are
// asked about in batches.
func TestFindAutoModeNLBBatchesTags(t *testing.T) {
	t.Parallel()

	calls, most := 0, 0
	fake := fakeLBs{tags: map[string][]elbv2types.Tag{}, tagCalls: &calls, maxARNs: &most}

	for i := range 45 {
		arn := fmt.Sprintf("arn:%d", i)
		fake.lbs = append(fake.lbs, lb(arn, fmt.Sprintf("%d.elb", i)))
	}

	fake.tags["arn:44"] = tags(autolb.TagServiceStack, "ns/svc", autolb.TagClusterName, "one")

	got, err := autolb.FindAutoModeNLB(context.Background(), fake, "one", "ns/svc")
	if err != nil || got != "44.elb" {
		t.Fatalf("dns=%q err=%v", got, err)
	}

	if calls != 3 || most != 20 {
		t.Fatalf("DescribeTags calls=%d most ARNs=%d, want 3 and 20", calls, most)
	}
}
