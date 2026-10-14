package ekscluster

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/route53"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The optional children around the API endpoint and the node role's registry
// access: Args.APIIngressCIDRs, Args.APIRecord and Args.NodeECR.

const (
	// KindAPIIngress is the ec2.SecurityGroupRule admitting one of
	// Args.APIIngressCIDRs on 443; Child.Key is its index ("0", "1", ...).
	KindAPIIngress Kind = "api-ingress"
	// KindAPIRecord is the route53.Record of Args.APIRecord.
	KindAPIRecord Kind = "api-record"

	// APIIngressDescription is the description of every API ingress rule.
	APIIngressDescription = "HTTPS to the private EKS endpoint"
	// DefaultAPIRecordTTL is APIRecord.TTL when it is zero.
	DefaultAPIRecordTTL = 300

	// NodeECRCrossAccountPolicy is the node role's inline policy NodeECR adds
	// for other accounts' registries (Child.Key of its KindNodeInlinePolicy).
	NodeECRCrossAccountPolicy = "cross-account-ecr-pull"
	// NodeECRPullThroughPolicy is the node role's inline policy NodeECR adds
	// for the pull-through cache.
	NodeECRPullThroughPolicy = "ptc-import"

	// DefaultPartition is NodeECR.Partition when it is empty.
	DefaultPartition = "aws"
)

// APIRecord publishes a CNAME to the cluster's API endpoint host (the
// endpoint without "https://") in a Route 53 zone: a stable name for the
// private endpoint, say.
type APIRecord struct {
	// ZoneID is the hosted zone. Required.
	ZoneID string
	// Name is the record's name. Required.
	Name string
	// TTL in seconds. Zero means DefaultAPIRecordTTL.
	TTL int
}

func (r *APIRecord) ttl() int {
	if r.TTL == 0 {
		return DefaultAPIRecordTTL
	}

	return r.TTL
}

// Registry is one account's ECR registry in one region.
type Registry struct {
	Region    string
	AccountID string
}

// PullThroughCache is the cluster account's ECR pull-through cache: the
// repositories under each prefix are created on the first pull of an image
// through it.
type PullThroughCache struct {
	Registry
	// Prefixes are the cache rules' repository prefixes. At least one.
	Prefixes []string
}

// NodeECR grants the node role what the managed pull-only policy does not
// cover, as inline policies:
//
//   - NodeECRCrossAccountPolicy, when CrossAccount is set: pulling images from
//     other accounts' registries (the managed policy covers the cluster's own
//     account only).
//   - NodeECRPullThroughPolicy, when PullThroughCache is set: creating a
//     cached repository and importing the upstream image on the first pull of
//     a new tag through the cache, which pull-only does not allow either.
//
// The documents are rendered by the provider's aws:iam/getPolicyDocument, so
// a node role whose inline policies were rendered that way adopts them with
// no change.
type NodeECR struct {
	// Partition is the ARN partition. Empty means DefaultPartition.
	Partition string
	// CrossAccount are the registries of other accounts the nodes pull from.
	CrossAccount []Registry
	// PullThroughCache is the cluster account's pull-through cache.
	PullThroughCache *PullThroughCache
}

func (n *NodeECR) partition() string {
	if n.Partition == "" {
		return DefaultPartition
	}

	return n.Partition
}

// policyNames are the inline policies n adds, in registration order.
func (n *NodeECR) policyNames() []string {
	if n == nil {
		return nil
	}

	var out []string

	if len(n.CrossAccount) > 0 {
		out = append(out, NodeECRCrossAccountPolicy)
	}

	if n.PullThroughCache != nil {
		out = append(out, NodeECRPullThroughPolicy)
	}

	return out
}

func (n *NodeECR) repositoryARN(r Registry, repository string) string {
	return fmt.Sprintf("arn:%s:ecr:%s:%s:repository/%s", n.partition(), r.Region, r.AccountID, repository)
}

// statements are the documents' statements, by inline policy name.
func (n *NodeECR) statements() map[string][]iam.GetPolicyDocumentStatement {
	out := map[string][]iam.GetPolicyDocumentStatement{}

	if len(n.CrossAccount) > 0 {
		images := make([]string, 0, len(n.CrossAccount))
		for _, r := range n.CrossAccount {
			images = append(images, n.repositoryARN(r, "*"))
		}

		out[NodeECRCrossAccountPolicy] = []iam.GetPolicyDocumentStatement{
			{
				Sid:       pulumi.StringRef("CrossAccountECRPull"),
				Effect:    pulumi.StringRef("Allow"),
				Actions:   []string{"ecr:GetAuthorizationToken"},
				Resources: []string{"*"},
			},
			{
				Sid:       pulumi.StringRef("CrossAccountECRImages"),
				Effect:    pulumi.StringRef("Allow"),
				Actions:   []string{"ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"},
				Resources: images,
			},
		}
	}

	if c := n.PullThroughCache; c != nil {
		repos := make([]string, 0, len(c.Prefixes))
		for _, p := range c.Prefixes {
			repos = append(repos, n.repositoryARN(c.Registry, p+"/*"))
		}

		out[NodeECRPullThroughPolicy] = []iam.GetPolicyDocumentStatement{{
			Sid:       pulumi.StringRef("PullThroughCacheImport"),
			Effect:    pulumi.StringRef("Allow"),
			Actions:   []string{"ecr:BatchImportUpstreamImage", "ecr:CreateRepository"},
			Resources: repos,
		}}
	}

	return out
}

func (n *NodeECR) validate() []error {
	var errs []error

	checkRegistry := func(field string, r Registry) {
		if r.Region == "" {
			errs = append(errs, fmt.Errorf("args: NodeECR.%s.Region is empty", field))
		}

		if r.AccountID == "" {
			errs = append(errs, fmt.Errorf("args: NodeECR.%s.AccountID is empty", field))
		}
	}

	seen := map[Registry]bool{}

	for i, r := range n.CrossAccount {
		checkRegistry(fmt.Sprintf("CrossAccount[%d]", i), r)

		if seen[r] {
			errs = append(errs, fmt.Errorf("args: NodeECR.CrossAccount[%d]: duplicate registry", i))
		}

		seen[r] = true
	}

	if c := n.PullThroughCache; c != nil {
		checkRegistry("PullThroughCache", c.Registry)

		if len(c.Prefixes) == 0 {
			errs = append(errs, errors.New("args: NodeECR.PullThroughCache.Prefixes is empty"))
		}

		prefixes := map[string]bool{}

		for i, p := range c.Prefixes {
			switch {
			case p == "" || strings.ContainsAny(p, "*/"):
				errs = append(errs, fmt.Errorf("args: NodeECR.PullThroughCache.Prefixes[%d] %q is not a repository prefix", i, p))
			case prefixes[p]:
				errs = append(errs, fmt.Errorf("args: NodeECR.PullThroughCache.Prefixes[%d]: duplicate %q", i, p))
			}

			prefixes[p] = true
		}
	}

	if len(n.CrossAccount) == 0 && n.PullThroughCache == nil {
		errs = append(errs, errors.New("args: NodeECR grants nothing: set CrossAccount or PullThroughCache, or leave NodeECR nil"))
	}

	return errs
}

// checkEndpoint validates APIIngressCIDRs, APIRecord and NodeECR.
func (a *Args) checkEndpoint() []error {
	var errs []error

	seen := map[netip.Prefix]bool{}

	for i, c := range a.APIIngressCIDRs {
		p, err := netip.ParsePrefix(c)

		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("args: APIIngressCIDRs[%d] %q: %w", i, c, err))
		case !p.Addr().Is4():
			errs = append(errs, fmt.Errorf("args: APIIngressCIDRs[%d] %q is not IPv4", i, c))
		case p.Masked() != p:
			errs = append(errs, fmt.Errorf("args: APIIngressCIDRs[%d] %q has host bits set (want %s)", i, c, p.Masked()))
		case seen[p]:
			errs = append(errs, fmt.Errorf("args: APIIngressCIDRs[%d]: duplicate %q", i, c))
		}

		seen[p] = true
	}

	if r := a.APIRecord; r != nil {
		if r.ZoneID == "" {
			errs = append(errs, errors.New("args: APIRecord.ZoneID is empty"))
		}

		if r.Name == "" {
			errs = append(errs, errors.New("args: APIRecord.Name is empty"))
		}

		if r.TTL < 0 {
			errs = append(errs, fmt.Errorf("args: APIRecord.TTL %d is negative", r.TTL))
		}
	}

	if a.NodeECR != nil {
		errs = append(errs, a.NodeECR.validate()...)

		for _, name := range a.NodeECR.policyNames() {
			for i, p := range a.NodeInlinePolicies {
				if p.Name == name {
					errs = append(errs, fmt.Errorf("args: NodeInlinePolicies[%d]: %q is the name of a NodeECR policy", i, name))
				}
			}
		}
	}

	return errs
}

// endpointPlan lists the API children, in registration order.
func (a *Args) endpointPlan(component string) []Child {
	var out []Child

	for i := range a.APIIngressCIDRs {
		out = append(out, Child{Component: component, Kind: KindAPIIngress, Key: strconv.Itoa(i)})
	}

	if a.APIRecord != nil {
		out = append(out, Child{Component: component, Kind: KindAPIRecord})
	}

	return out
}

// nodeECRPolicies renders NodeECR's inline policies.
func (b *builder) nodeECRPolicies() ([]InlinePolicy, error) {
	n := b.args.NodeECR
	if n == nil {
		return nil, nil
	}

	statements := n.statements()

	var out []InlinePolicy

	for _, name := range n.policyNames() {
		doc, err := iam.GetPolicyDocument(b.ctx, &iam.GetPolicyDocumentArgs{Statements: statements[name]}, pulumi.Provider(b.args.Provider))
		if err != nil {
			return nil, fmt.Errorf("render node policy %s: %w", name, err)
		}

		out = append(out, InlinePolicy{Name: name, Document: pulumi.String(doc.Json)})
	}

	return out, nil
}

// endpoint registers the API ingress rules and the API record.
func (b *builder) endpoint(cluster *eks.Cluster) error {
	a := b.args

	for i, cidr := range a.APIIngressCIDRs {
		key := strconv.Itoa(i)

		if _, err := ec2.NewSecurityGroupRule(b.ctx, b.child(Child{Kind: KindAPIIngress, Key: key}), &ec2.SecurityGroupRuleArgs{
			Type:            pulumi.String("ingress"),
			SecurityGroupId: cluster.VpcConfig.ClusterSecurityGroupId().Elem(),
			Protocol:        pulumi.String("tcp"),
			FromPort:        pulumi.Int(443),
			ToPort:          pulumi.Int(443),
			CidrBlocks:      pulumi.StringArray{pulumi.String(cidr)},
			Description:     pulumi.String(APIIngressDescription),
		}, b.childOptsFor(Child{Kind: KindAPIIngress, Key: key}, false, pulumi.DependsOn([]pulumi.Resource{cluster}))...); err != nil {
			return fmt.Errorf("create API ingress rule %s: %w", cidr, err)
		}
	}

	r := a.APIRecord
	if r == nil {
		return nil
	}

	host := cluster.Endpoint.ApplyT(func(endpoint string) string {
		return strings.TrimPrefix(endpoint, "https://")
	}).(pulumi.StringOutput)

	if _, err := route53.NewRecord(b.ctx, b.child(Child{Kind: KindAPIRecord}), &route53.RecordArgs{
		ZoneId:  pulumi.String(r.ZoneID),
		Name:    pulumi.String(r.Name),
		Type:    pulumi.String("CNAME"),
		Ttl:     pulumi.Int(r.ttl()),
		Records: pulumi.StringArray{host},
	}, b.childOptsFor(Child{Kind: KindAPIRecord}, false)...); err != nil {
		return fmt.Errorf("create API record: %w", err)
	}

	return nil
}
