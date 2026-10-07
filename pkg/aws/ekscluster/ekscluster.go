package ekscluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:k8s/aws:EksCluster"

// Defaults of the optional Args fields.
const (
	// DefaultLogRetentionDays is the control-plane log group's retention.
	DefaultLogRetentionDays = 90
	// DefaultKeyRotationDays is the KMS key's rotation period.
	DefaultKeyRotationDays = 365

	minKeyRotationDays = 90
	maxKeyRotationDays = 2560
)

// DefaultNodePools are the EKS-managed node pools enabled when Args.NodePools
// is empty.
func DefaultNodePools() []string { return []string{"general-purpose", "system"} }

// DefaultLogTypes are the control-plane log types enabled when
// Args.LogTypes is empty: all of them.
func DefaultLogTypes() []string {
	return []string{"api", "audit", "authenticator", "controllerManager", "scheduler"}
}

// Kind names which child a logical name is for.
type Kind string

const (
	// KindKey is the kms.Key the cluster's secrets are encrypted with.
	KindKey Kind = "key"
	// KindKeyAlias is the kms.Alias of that key.
	KindKeyAlias Kind = "key-alias"
	// KindClusterRole is the cluster's iam.Role.
	KindClusterRole Kind = "cluster-role"
	// KindClusterPolicy is the iam.RolePolicyAttachment of one of
	// Args.ClusterPolicies; Child.Key is the last path segment of its ARN.
	KindClusterPolicy Kind = "cluster-policy"
	// KindNodeRole is the Auto Mode node iam.Role.
	KindNodeRole Kind = "node-role"
	// KindNodePolicy is the iam.RolePolicyAttachment of one of
	// Args.NodePolicies; Child.Key is the last path segment of its ARN.
	KindNodePolicy Kind = "node-policy"
	// KindNodeInlinePolicy is the iam.RolePolicy built from one of
	// Args.NodeInlinePolicies; Child.Key is its Name.
	KindNodeInlinePolicy Kind = "node-inline-policy"
	// KindLogGroup is the cloudwatch.LogGroup of the control-plane logs.
	KindLogGroup Kind = "log-group"
	// KindCluster is the eks.Cluster.
	KindCluster Kind = "cluster"
	// KindAccessEntry is the eks.AccessEntry of one of Args.AccessEntries;
	// Child.Key is its Name.
	KindAccessEntry Kind = "access-entry"
	// KindAccessPolicy is the eks.AccessPolicyAssociation of one of
	// Args.AccessEntries; Child.Key is its Name.
	KindAccessPolicy Kind = "access-policy"
	// KindCoreDNS is the coredns eks.Addon (Args.CoreDNS).
	KindCoreDNS Kind = "coredns"
)

// Child identifies one child for a NameFunc.
type Child struct {
	// Component is the logical name the component was registered with.
	Component string
	Kind      Kind
	// Key says which policy a KindClusterPolicy, KindNodePolicy or
	// KindNodeInlinePolicy is for, and which access entry a KindAccessEntry
	// or KindAccessPolicy is for. Empty otherwise.
	Key string
}

// NameFunc returns the Pulumi logical name of a child. The returned name must
// be unique among the component's children; it is part of the child's URN.
type NameFunc func(Child) string

// DefaultName names the children from the component name: "<c>-key",
// "<c>-key-alias", "<c>-cluster-role", "<c>-cluster-role-<key>",
// "<c>-node-role", "<c>-node-role-<key>", "<c>-node-role-policy-<name>",
// "<c>-logs", "<c>-cluster", "<c>-access-<name>", "<c>-access-<name>-policy"
// and "<c>-coredns". These names are API.
func DefaultName(c Child) string {
	switch c.Kind {
	case KindKey:
		return c.Component + "-key"
	case KindKeyAlias:
		return c.Component + "-key-alias"
	case KindClusterRole:
		return c.Component + "-cluster-role"
	case KindClusterPolicy:
		return c.Component + "-cluster-role-" + c.Key
	case KindNodeRole:
		return c.Component + "-node-role"
	case KindNodePolicy:
		return c.Component + "-node-role-" + c.Key
	case KindNodeInlinePolicy:
		return c.Component + "-node-role-policy-" + c.Key
	case KindLogGroup:
		return c.Component + "-logs"
	case KindAccessEntry:
		return c.Component + "-access-" + c.Key
	case KindAccessPolicy:
		return c.Component + "-access-" + c.Key + "-policy"
	case KindCoreDNS:
		return c.Component + "-coredns"
	default:
		return c.Component + "-cluster"
	}
}

// InlinePolicy is a policy embedded in the node role (iam.RolePolicy).
type InlinePolicy struct {
	// Name is the inline policy's name within the role. Required, unique.
	Name string
	// Document is the policy JSON. Required.
	Document pulumi.StringInput
}

// AccessEntry grants one IAM principal access to the cluster through the API
// authentication mode: an eks.AccessEntry of type STANDARD and the
// eks.AccessPolicyAssociation of one EKS access policy.
type AccessEntry struct {
	// Name identifies the entry among the component's (Child.Key). Required,
	// unique.
	Name string
	// PrincipalARN is the IAM principal (role or user) given access.
	// Required, unique: EKS admits one entry per principal.
	PrincipalARN string
	// PolicyARN is the ARN of the EKS access policy associated with the
	// principal (AmazonEKSClusterAdminPolicy, say). Required.
	PolicyARN string
	// Namespaces scopes the association to these namespaces. Empty: the
	// whole cluster.
	Namespaces []string
}

// CoreDNS installs the coredns EKS add-on with a Corefile the component
// renders: Corefile with every line of Extras inserted above its kubernetes
// stanza. The add-on's configuration replaces the WHOLE Corefile, so Corefile
// must be the stock file of Version.
//
// On an Auto Mode cluster the add-on serves nothing through the cluster DNS
// address: the node-local resolver answers it first. It is for a cluster
// that is not Auto Mode, or a resolver queried directly.
type CoreDNS struct {
	// Version is the add-on version. Empty means DefaultCoreDNSVersion.
	Version string
	// Corefile is the stock Corefile of Version. Empty means
	// StockCorefile, which is the stock file of DefaultCoreDNSVersion only:
	// another Version with no Corefile is refused.
	Corefile string
	// Extras are Corefile lines (one directive each, no newline) inserted, in
	// order and indented like the stock directives, above the kubernetes
	// stanza. CoreDNS runs plugins in its compiled order, so where a line
	// sits does not change when it runs.
	Extras []string
}

// DefaultCoreDNSVersion is the coredns add-on version CoreDNS.Version
// defaults to, and the version StockCorefile is the stock file of.
const DefaultCoreDNSVersion = "v1.14.3-eksbuild.14"

// StockCorefile is DefaultCoreDNSVersion's stock Corefile, byte for byte as
// the freshly installed add-on writes it to the kube-system/coredns
// ConfigMap, odd indentation included.
const StockCorefile = `.:53 {
    errors
    health {
        lameduck 5s
      }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
      pods insecure
      fallthrough in-addr.arpa ip6.arpa
    }
    prometheus :9153
    forward . /etc/resolv.conf
    cache 30
    loop
    reload
    loadbalance
}`

// corefileAnchor is the line start the Extras are inserted above.
const corefileAnchor = "    kubernetes "

func (c *CoreDNS) version() string {
	if c.Version != "" {
		return c.Version
	}

	return DefaultCoreDNSVersion
}

func (c *CoreDNS) corefile() string {
	if c.Corefile != "" {
		return c.Corefile
	}

	return StockCorefile
}

// configurationValues renders the add-on's configurationValues JSON:
// {"corefile": <Corefile with Extras above the kubernetes stanza>}.
func (c *CoreDNS) configurationValues() (string, error) {
	base := c.corefile()

	var lines strings.Builder
	for _, e := range c.Extras {
		lines.WriteString("    " + e + "\n")
	}

	corefile := strings.Replace(base, corefileAnchor, lines.String()+corefileAnchor, 1)

	raw, err := json.Marshal(map[string]string{"corefile": corefile})
	if err != nil {
		return "", fmt.Errorf("render coredns configuration values: %w", err)
	}

	return string(raw), nil
}

func (c *CoreDNS) validate() []error {
	var errs []error

	if c.Version != "" && c.Version != DefaultCoreDNSVersion && c.Corefile == "" {
		errs = append(errs, fmt.Errorf(
			"args: CoreDNS.Version %q needs its stock CoreDNS.Corefile: StockCorefile is %s's, and the configuration replaces the whole file",
			c.Version, DefaultCoreDNSVersion))
	}

	if len(c.Extras) > 0 && !strings.Contains(c.corefile(), corefileAnchor) {
		errs = append(errs, fmt.Errorf("args: CoreDNS.Corefile has no %q stanza to insert the extras above", strings.TrimSpace(corefileAnchor)))
	}

	for i, e := range c.Extras {
		switch {
		case strings.TrimSpace(e) == "":
			errs = append(errs, fmt.Errorf("args: CoreDNS.Extras[%d] is empty", i))
		case strings.ContainsAny(e, "\r\n"):
			errs = append(errs, fmt.Errorf("args: CoreDNS.Extras[%d] spans more than one line", i))
		}
	}

	return errs
}

// Args configures the component.
type Args struct {
	// Provider is the AWS provider of the cluster's account and region.
	// Required: the component never falls back to a default provider.
	Provider pulumi.ProviderResource

	// Name is the cluster's name. Required.
	Name string
	// Version is the Kubernetes version, for example "1.31". Required.
	Version string
	// SubnetIDs are the subnets the control plane's network interfaces and
	// the nodes use. Required, no repeats.
	SubnetIDs []string
	// ServiceCIDR is the cluster's service IPv4 range. It cannot be changed
	// once the cluster exists: another value replaces the cluster. Required.
	ServiceCIDR string

	// PublicEndpoint opens the API endpoint to the internet. False (the
	// default) leaves it private-only.
	PublicEndpoint bool
	// NodePools are the EKS-managed node pools. Empty means
	// DefaultNodePools.
	NodePools []string
	// LogTypes are the control-plane log types sent to the log group. Empty
	// means DefaultLogTypes.
	LogTypes []string

	// PermissionsBoundary is the ARN of the permissions boundary of both
	// roles. Empty sets none.
	PermissionsBoundary string
	// ClusterRoleName and NodeRoleName are the IAM role names. Empty means
	// "<Name>-cluster" and "<Name>-node".
	ClusterRoleName string
	NodeRoleName    string
	// ClusterTrustPolicy and NodeTrustPolicy replace the default trust
	// documents (the EKS service may assume the cluster role, with
	// sts:AssumeRole and sts:TagSession; the EC2 service the node role, with
	// sts:AssumeRole) and are used verbatim. Use them to adopt a role whose
	// document was rendered another way: IAM compares the JSON, but the
	// provider compares the string, so a re-rendered document is an in-place
	// update of the role.
	ClusterTrustPolicy pulumi.StringInput
	NodeTrustPolicy    pulumi.StringInput

	// ClusterPolicies are the ARNs of the managed policies attached to the
	// cluster role. Required: a cluster role with no policy cannot run a
	// cluster. The caller passes ARNs; the component holds none.
	ClusterPolicies []string
	// NodePolicies are the ARNs of the managed policies attached to the
	// node role.
	NodePolicies []string
	// NodeInlinePolicies are inline policies of the node role.
	NodeInlinePolicies []InlinePolicy

	// KeyDescription and KeyAlias describe the secrets key. Empty means
	// "EKS <Name> cluster secrets encryption" and "alias/eks-<Name>-secrets".
	KeyDescription string
	KeyAlias       string
	// KeyRotationDays is the key's rotation period, 90 to 2560. Zero means
	// DefaultKeyRotationDays. Rotation is always on.
	KeyRotationDays int
	// LogGroupName is the control-plane log group's name. EKS writes to
	// "/aws/eks/<Name>/cluster", which is the default. Another name leaves
	// the cluster's logs outside the group.
	LogGroupName string
	// LogRetentionDays is the log group's retention. Zero means
	// DefaultLogRetentionDays.
	LogRetentionDays int

	// AccessEntries are the principals given access to the cluster through
	// the API authentication mode, each an access entry and one access policy
	// association. The cluster has no other admin: with no entry, nobody
	// reaches its API.
	AccessEntries []AccessEntry
	// CoreDNS installs the coredns add-on. Nil installs none.
	CoreDNS *CoreDNS

	// Tags are set on the key, the roles, the log group and the cluster.
	Tags map[string]string

	// Names overrides the logical names of the children. Nil uses
	// DefaultName.
	Names NameFunc
	// LegacyTopLevel makes every child carry an alias from the URN it has
	// when it is registered directly under the stack (no parent), with the
	// same type and the name Names gives it. Set it when adopting resources
	// that were created before they were wrapped in this component.
	LegacyTopLevel bool
	// Protect marks the cluster, the KMS key and the two roles protected, so
	// a preview that would delete or replace them fails. Nil (the default)
	// protects them; point it at false only for a cluster that is meant to be
	// torn down.
	Protect *bool
}

func (a *Args) protect() bool { return a.Protect == nil || *a.Protect }

func (a *Args) clusterRoleName() string {
	if a.ClusterRoleName != "" {
		return a.ClusterRoleName
	}

	return a.Name + "-cluster"
}

func (a *Args) nodeRoleName() string {
	if a.NodeRoleName != "" {
		return a.NodeRoleName
	}

	return a.Name + "-node"
}

func (a *Args) keyDescription() string {
	if a.KeyDescription != "" {
		return a.KeyDescription
	}

	return fmt.Sprintf("EKS %s cluster secrets encryption", a.Name)
}

func (a *Args) keyAlias() string {
	if a.KeyAlias != "" {
		return a.KeyAlias
	}

	return fmt.Sprintf("alias/eks-%s-secrets", a.Name)
}

func (a *Args) keyRotationDays() int {
	if a.KeyRotationDays == 0 {
		return DefaultKeyRotationDays
	}

	return a.KeyRotationDays
}

func (a *Args) logGroupName() string {
	if a.LogGroupName != "" {
		return a.LogGroupName
	}

	return fmt.Sprintf("/aws/eks/%s/cluster", a.Name)
}

func (a *Args) logRetentionDays() int {
	if a.LogRetentionDays == 0 {
		return DefaultLogRetentionDays
	}

	return a.LogRetentionDays
}

func (a *Args) nodePools() []string {
	if len(a.NodePools) == 0 {
		return DefaultNodePools()
	}

	return a.NodePools
}

func (a *Args) logTypes() []string {
	if len(a.LogTypes) == 0 {
		return DefaultLogTypes()
	}

	return a.LogTypes
}

// EksCluster is the component.
type EksCluster struct {
	pulumi.ResourceState

	// Cluster is the eks.Cluster, for what the caller attaches to it (access
	// entries, security group rules, add-ons).
	Cluster *eks.Cluster
	// KeyARN is the secrets key's ARN.
	KeyARN pulumi.StringOutput
	// ClusterRoleARN, ClusterRoleName, NodeRoleARN and NodeRoleName are the
	// two roles'.
	ClusterRoleARN  pulumi.StringOutput
	ClusterRoleName pulumi.StringOutput
	NodeRoleARN     pulumi.StringOutput
	NodeRoleName    pulumi.StringOutput
}

// policyKey is the last path segment of a policy ARN: the policy's name.
func policyKey(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}

	return arn
}

// plan lists every child the args register, in registration order.
func (a *Args) plan(component string) []Child {
	out := []Child{
		{Component: component, Kind: KindKey},
		{Component: component, Kind: KindKeyAlias},
		{Component: component, Kind: KindClusterRole},
	}

	for _, p := range a.ClusterPolicies {
		out = append(out, Child{Component: component, Kind: KindClusterPolicy, Key: policyKey(p)})
	}

	out = append(out, Child{Component: component, Kind: KindNodeRole})

	for _, p := range a.NodePolicies {
		out = append(out, Child{Component: component, Kind: KindNodePolicy, Key: policyKey(p)})
	}

	for _, p := range a.NodeInlinePolicies {
		out = append(out, Child{Component: component, Kind: KindNodeInlinePolicy, Key: p.Name})
	}

	out = append(out,
		Child{Component: component, Kind: KindLogGroup},
		Child{Component: component, Kind: KindCluster},
	)

	for _, e := range a.AccessEntries {
		out = append(out,
			Child{Component: component, Kind: KindAccessEntry, Key: e.Name},
			Child{Component: component, Kind: KindAccessPolicy, Key: e.Name},
		)
	}

	if a.CoreDNS != nil {
		out = append(out, Child{Component: component, Kind: KindCoreDNS})
	}

	return out
}

// Validate reports every problem with args at once, or returns nil.
func (a *Args) Validate() error {
	var errs []error

	if a.Provider == nil {
		errs = append(errs, errors.New("args: Provider is nil"))
	}

	if a.Name == "" {
		errs = append(errs, errors.New("args: Name is empty"))
	}

	if a.Version == "" {
		errs = append(errs, errors.New("args: Version is empty"))
	}

	errs = append(errs, a.checkSubnets()...)
	errs = append(errs, a.checkServiceCIDR()...)

	if len(a.ClusterPolicies) == 0 {
		errs = append(errs, errors.New("args: ClusterPolicies is empty: a cluster role with no policy cannot run a cluster"))
	}

	errs = append(errs, checkARNs("ClusterPolicies", a.ClusterPolicies)...)
	errs = append(errs, checkARNs("NodePolicies", a.NodePolicies)...)

	seen := map[string]bool{}

	for i, p := range a.NodeInlinePolicies {
		switch {
		case p.Name == "":
			errs = append(errs, fmt.Errorf("args: NodeInlinePolicies[%d].Name is empty", i))
		case seen[p.Name]:
			errs = append(errs, fmt.Errorf("args: NodeInlinePolicies[%d]: duplicate name %q", i, p.Name))
		}

		seen[p.Name] = true

		if p.Document == nil {
			errs = append(errs, fmt.Errorf("args: NodeInlinePolicies[%d].Document is nil", i))
		}
	}

	if a.Name != "" && a.clusterRoleName() == a.nodeRoleName() {
		errs = append(errs, fmt.Errorf("args: the cluster role and the node role are both named %q", a.nodeRoleName()))
	}

	for i, t := range a.logTypes() {
		if !slices.Contains(DefaultLogTypes(), t) {
			errs = append(errs, fmt.Errorf("args: LogTypes[%d]: unknown log type %q", i, t))
		}
	}

	for i, p := range a.nodePools() {
		if p == "" {
			errs = append(errs, fmt.Errorf("args: NodePools[%d] is empty", i))
		}
	}

	if d := a.keyRotationDays(); d < minKeyRotationDays || d > maxKeyRotationDays {
		errs = append(errs, fmt.Errorf("args: KeyRotationDays %d is outside %d to %d", d, minKeyRotationDays, maxKeyRotationDays))
	}

	if a.LogRetentionDays < 0 {
		errs = append(errs, fmt.Errorf("args: LogRetentionDays %d is negative", a.LogRetentionDays))
	}

	errs = append(errs, a.checkAccessEntries()...)

	if a.CoreDNS != nil {
		errs = append(errs, a.CoreDNS.validate()...)
	}

	errs = append(errs, a.checkNames("")...)

	return errors.Join(errs...)
}

func (a *Args) checkAccessEntries() []error {
	var errs []error

	names, principals := map[string]bool{}, map[string]bool{}

	for i, e := range a.AccessEntries {
		switch {
		case e.Name == "":
			errs = append(errs, fmt.Errorf("args: AccessEntries[%d].Name is empty", i))
		case names[e.Name]:
			errs = append(errs, fmt.Errorf("args: AccessEntries[%d]: duplicate name %q", i, e.Name))
		}

		names[e.Name] = true

		switch {
		case e.PrincipalARN == "":
			errs = append(errs, fmt.Errorf("args: AccessEntries[%d].PrincipalARN is empty", i))
		case principals[e.PrincipalARN]:
			errs = append(errs, fmt.Errorf("args: AccessEntries[%d]: principal %q already has an entry (EKS admits one per principal)", i, e.PrincipalARN))
		}

		principals[e.PrincipalARN] = true

		if e.PolicyARN == "" {
			errs = append(errs, fmt.Errorf("args: AccessEntries[%d].PolicyARN is empty", i))
		}

		for j, ns := range e.Namespaces {
			if ns == "" {
				errs = append(errs, fmt.Errorf("args: AccessEntries[%d].Namespaces[%d] is empty", i, j))
			}
		}
	}

	return errs
}

func (a *Args) checkSubnets() []error {
	if len(a.SubnetIDs) == 0 {
		return []error{errors.New("args: SubnetIDs is empty")}
	}

	var errs []error

	seen := map[string]bool{}

	for i, s := range a.SubnetIDs {
		switch {
		case s == "":
			errs = append(errs, fmt.Errorf("args: SubnetIDs[%d] is empty", i))
		case seen[s]:
			errs = append(errs, fmt.Errorf("args: SubnetIDs[%d]: duplicate %q", i, s))
		}

		seen[s] = true
	}

	return errs
}

func (a *Args) checkServiceCIDR() []error {
	if a.ServiceCIDR == "" {
		return []error{errors.New("args: ServiceCIDR is empty (it is create-time immutable)")}
	}

	p, err := netip.ParsePrefix(a.ServiceCIDR)
	if err != nil {
		return []error{fmt.Errorf("args: ServiceCIDR %q: %w", a.ServiceCIDR, err)}
	}

	if !p.Addr().Is4() {
		return []error{fmt.Errorf("args: ServiceCIDR %q is not IPv4", a.ServiceCIDR)}
	}

	if p.Masked() != p {
		return []error{fmt.Errorf("args: ServiceCIDR %q has host bits set (want %s)", a.ServiceCIDR, p.Masked())}
	}

	return nil
}

func checkARNs(field string, arns []string) []error {
	var errs []error

	seen := map[string]bool{}

	for i, p := range arns {
		switch {
		case p == "":
			errs = append(errs, fmt.Errorf("args: %s[%d] is empty", field, i))
		case seen[p]:
			errs = append(errs, fmt.Errorf("args: %s[%d]: duplicate %q", field, i, p))
		}

		seen[p] = true
	}

	return errs
}

func (a *Args) nameFunc() NameFunc {
	if a.Names == nil {
		return DefaultName
	}

	return a.Names
}

// checkNames refuses a naming hook that returns an empty or repeated name.
func (a *Args) checkNames(component string) []error {
	names := a.nameFunc()

	var errs []error

	used := map[string]Child{}

	for _, c := range a.plan(component) {
		n := names(c)
		if n == "" {
			errs = append(errs, fmt.Errorf("args: Names returned an empty name for %s", describe(c)))
			continue
		}

		if other, dup := used[n]; dup {
			errs = append(errs, fmt.Errorf("args: Names returned %q for both %s and %s", n, describe(other), describe(c)))
		}

		used[n] = c
	}

	return errs
}

func describe(c Child) string {
	if c.Key != "" {
		return string(c.Kind) + " " + c.Key
	}

	return string(c.Kind)
}

// trust renders a trust document for one service principal.
func trust(service string, actions ...string) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": service},
			"Action":    actions,
		}},
	})
	if err != nil {
		return "", fmt.Errorf("render trust policy: %w", err)
	}

	return string(raw), nil
}

func (a *Args) trusts() (cluster, node pulumi.StringInput, err error) {
	cluster, node = a.ClusterTrustPolicy, a.NodeTrustPolicy

	if cluster == nil {
		doc, err := trust("eks.amazonaws.com", "sts:AssumeRole", "sts:TagSession")
		if err != nil {
			return nil, nil, err
		}

		cluster = pulumi.String(doc)
	}

	if node == nil {
		doc, err := trust("ec2.amazonaws.com", "sts:AssumeRole")
		if err != nil {
			return nil, nil, err
		}

		node = pulumi.String(doc)
	}

	return cluster, node, nil
}

// New registers the component and its children. It returns an error,
// registering nothing, when args.Validate does or when Names returns an empty
// or repeated name.
//
// The provider comes from Args, not from pulumi.Providers.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*EksCluster, error) {
	if args == nil {
		return nil, errors.New("ekscluster: args is nil")
	}

	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("ekscluster %s: %w", name, err)
	}

	if err := errors.Join(args.checkNames(name)...); err != nil {
		return nil, fmt.Errorf("ekscluster %s: %w", name, err)
	}

	clusterTrust, nodeTrust, err := args.trusts()
	if err != nil {
		return nil, fmt.Errorf("ekscluster %s: %w", name, err)
	}

	comp := &EksCluster{}
	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	b := &builder{ctx: ctx, comp: comp, args: args, name: name, names: args.nameFunc()}
	if err := b.build(clusterTrust, nodeTrust); err != nil {
		return nil, err
	}

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{
		"clusterName":     comp.Cluster.Name,
		"keyArn":          comp.KeyARN,
		"clusterRoleArn":  comp.ClusterRoleARN,
		"clusterRoleName": comp.ClusterRoleName,
		"nodeRoleArn":     comp.NodeRoleARN,
		"nodeRoleName":    comp.NodeRoleName,
	}); err != nil {
		return nil, err
	}

	return comp, nil
}

type builder struct {
	ctx   *pulumi.Context
	comp  *EksCluster
	args  *Args
	name  string
	names NameFunc
}

func (b *builder) child(c Child) string {
	c.Component = b.name

	return b.names(c)
}

// childOpts is what every child registers with: the component as parent, the
// provider, the adoption alias when asked for, and protection when asked for.
func (b *builder) childOpts(protect bool, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := append([]pulumi.ResourceOption{pulumi.Parent(b.comp), pulumi.Provider(b.args.Provider)}, extra...)

	if b.args.LegacyTopLevel {
		out = append(out, pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}}))
	}

	if protect && b.args.protect() {
		out = append(out, pulumi.Protect(true))
	}

	return out
}

func (b *builder) tags() pulumi.StringMapInput {
	if len(b.args.Tags) == 0 {
		return nil
	}

	return pulumi.ToStringMap(b.args.Tags)
}

func (b *builder) role(kind Kind, roleName string, trust pulumi.StringInput) (*iam.Role, error) {
	args := &iam.RoleArgs{
		Name:             pulumi.String(roleName),
		AssumeRolePolicy: trust,
		Tags:             b.tags(),
	}
	if b.args.PermissionsBoundary != "" {
		args.PermissionsBoundary = pulumi.String(b.args.PermissionsBoundary)
	}

	return iam.NewRole(b.ctx, b.child(Child{Kind: kind}), args, b.childOpts(true)...)
}

func (b *builder) attach(kind Kind, role *iam.Role, arns []string) ([]pulumi.Resource, error) {
	var out []pulumi.Resource

	for _, arn := range arns {
		att, err := iam.NewRolePolicyAttachment(b.ctx, b.child(Child{Kind: kind, Key: policyKey(arn)}), &iam.RolePolicyAttachmentArgs{
			Role:      role.Name,
			PolicyArn: pulumi.String(arn),
		}, b.childOpts(false)...)
		if err != nil {
			return nil, fmt.Errorf("attach %s: %w", arn, err)
		}

		out = append(out, att)
	}

	return out, nil
}

func (b *builder) build(clusterTrust, nodeTrust pulumi.StringInput) error {
	a := b.args
	c := b.comp

	key, err := kms.NewKey(b.ctx, b.child(Child{Kind: KindKey}), &kms.KeyArgs{
		Description:          pulumi.String(a.keyDescription()),
		EnableKeyRotation:    pulumi.Bool(true),
		RotationPeriodInDays: pulumi.Int(a.keyRotationDays()),
		Tags:                 b.tags(),
	}, b.childOpts(true)...)
	if err != nil {
		return fmt.Errorf("create key: %w", err)
	}

	c.KeyARN = key.Arn

	if _, err := kms.NewAlias(b.ctx, b.child(Child{Kind: KindKeyAlias}), &kms.AliasArgs{
		Name:        pulumi.String(a.keyAlias()),
		TargetKeyId: key.ID(),
	}, b.childOpts(false)...); err != nil {
		return fmt.Errorf("create key alias: %w", err)
	}

	clusterRole, err := b.role(KindClusterRole, a.clusterRoleName(), clusterTrust)
	if err != nil {
		return fmt.Errorf("create cluster role: %w", err)
	}

	c.ClusterRoleARN, c.ClusterRoleName = clusterRole.Arn, clusterRole.Name

	deps, err := b.attach(KindClusterPolicy, clusterRole, a.ClusterPolicies)
	if err != nil {
		return err
	}

	nodeRole, err := b.role(KindNodeRole, a.nodeRoleName(), nodeTrust)
	if err != nil {
		return fmt.Errorf("create node role: %w", err)
	}

	c.NodeRoleARN, c.NodeRoleName = nodeRole.Arn, nodeRole.Name

	nodeDeps, err := b.attach(KindNodePolicy, nodeRole, a.NodePolicies)
	if err != nil {
		return err
	}

	deps = append(deps, nodeDeps...)

	for _, p := range a.NodeInlinePolicies {
		if _, err := iam.NewRolePolicy(b.ctx, b.child(Child{Kind: KindNodeInlinePolicy, Key: p.Name}), &iam.RolePolicyArgs{
			Role:   nodeRole.Name,
			Name:   pulumi.String(p.Name),
			Policy: p.Document,
		}, b.childOpts(false)...); err != nil {
			return fmt.Errorf("create node inline policy %s: %w", p.Name, err)
		}
	}

	logGroup, err := cloudwatch.NewLogGroup(b.ctx, b.child(Child{Kind: KindLogGroup}), &cloudwatch.LogGroupArgs{
		Name:            pulumi.String(a.logGroupName()),
		RetentionInDays: pulumi.Int(a.logRetentionDays()),
		Tags:            b.tags(),
	}, b.childOpts(false)...)
	if err != nil {
		return fmt.Errorf("create log group: %w", err)
	}

	// The cluster is created after both roles, their policies and the log
	// group: EKS validates the role's policies and writes to the group.
	deps = append(deps, clusterRole, nodeRole, logGroup)

	cluster, err := eks.NewCluster(b.ctx, b.child(Child{Kind: KindCluster}), &eks.ClusterArgs{
		Name:    pulumi.String(a.Name),
		Version: pulumi.String(a.Version),
		RoleArn: clusterRole.Arn,
		VpcConfig: &eks.ClusterVpcConfigArgs{
			SubnetIds:             pulumi.ToStringArray(a.SubnetIDs),
			EndpointPrivateAccess: pulumi.Bool(true),
			EndpointPublicAccess:  pulumi.Bool(a.PublicEndpoint),
		},
		AccessConfig: &eks.ClusterAccessConfigArgs{
			AuthenticationMode:                      pulumi.String("API"),
			BootstrapClusterCreatorAdminPermissions: pulumi.Bool(false),
		},
		EncryptionConfig: &eks.ClusterEncryptionConfigArgs{
			Provider:  &eks.ClusterEncryptionConfigProviderArgs{KeyArn: key.Arn},
			Resources: pulumi.StringArray{pulumi.String("secrets")},
		},
		EnabledClusterLogTypes:     pulumi.ToStringArray(a.logTypes()),
		BootstrapSelfManagedAddons: pulumi.Bool(false),
		// Auto Mode: EKS manages compute, load balancing and block storage.
		ComputeConfig: &eks.ClusterComputeConfigArgs{
			Enabled:     pulumi.Bool(true),
			NodePools:   pulumi.ToStringArray(a.nodePools()),
			NodeRoleArn: nodeRole.Arn,
		},
		KubernetesNetworkConfig: &eks.ClusterKubernetesNetworkConfigArgs{
			ServiceIpv4Cidr: pulumi.String(a.ServiceCIDR),
			ElasticLoadBalancing: &eks.ClusterKubernetesNetworkConfigElasticLoadBalancingArgs{
				Enabled: pulumi.Bool(true),
			},
		},
		StorageConfig: &eks.ClusterStorageConfigArgs{
			BlockStorage: &eks.ClusterStorageConfigBlockStorageArgs{Enabled: pulumi.Bool(true)},
		},
		Tags: b.tags(),
	}, b.childOpts(true, pulumi.DependsOn(deps))...)
	if err != nil {
		return fmt.Errorf("create cluster: %w", err)
	}

	c.Cluster = cluster

	if err := b.accessEntries(cluster); err != nil {
		return err
	}

	return b.coreDNS(cluster)
}

func (b *builder) accessEntries(cluster *eks.Cluster) error {
	for _, e := range b.args.AccessEntries {
		entry, err := eks.NewAccessEntry(b.ctx, b.child(Child{Kind: KindAccessEntry, Key: e.Name}), &eks.AccessEntryArgs{
			ClusterName:  cluster.Name,
			PrincipalArn: pulumi.String(e.PrincipalARN),
			Type:         pulumi.String("STANDARD"),
		}, b.childOpts(false)...)
		if err != nil {
			return fmt.Errorf("create access entry %s: %w", e.Name, err)
		}

		scope := &eks.AccessPolicyAssociationAccessScopeArgs{Type: pulumi.String("cluster")}
		if len(e.Namespaces) > 0 {
			scope = &eks.AccessPolicyAssociationAccessScopeArgs{
				Type:       pulumi.String("namespace"),
				Namespaces: pulumi.ToStringArray(e.Namespaces),
			}
		}

		if _, err := eks.NewAccessPolicyAssociation(b.ctx, b.child(Child{Kind: KindAccessPolicy, Key: e.Name}), &eks.AccessPolicyAssociationArgs{
			ClusterName:  cluster.Name,
			PrincipalArn: pulumi.String(e.PrincipalARN),
			PolicyArn:    pulumi.String(e.PolicyARN),
			AccessScope:  scope,
		}, b.childOpts(false, pulumi.DependsOn([]pulumi.Resource{entry}))...); err != nil {
			return fmt.Errorf("create access policy association %s: %w", e.Name, err)
		}
	}

	return nil
}

// coreDNS installs the add-on. OVERWRITE both ways: on create because the
// fields of a CoreDNS already running must yield to the add-on, on update so
// the rendered Corefile is never half-merged with a default no longer seen.
func (b *builder) coreDNS(cluster *eks.Cluster) error {
	cd := b.args.CoreDNS
	if cd == nil {
		return nil
	}

	values, err := cd.configurationValues()
	if err != nil {
		return err
	}

	if _, err := eks.NewAddon(b.ctx, b.child(Child{Kind: KindCoreDNS}), &eks.AddonArgs{
		ClusterName:              cluster.Name,
		AddonName:                pulumi.String("coredns"),
		AddonVersion:             pulumi.String(cd.version()),
		ConfigurationValues:      pulumi.String(values),
		ResolveConflictsOnCreate: pulumi.String("OVERWRITE"),
		ResolveConflictsOnUpdate: pulumi.String("OVERWRITE"),
	}, b.childOpts(false)...); err != nil {
		return fmt.Errorf("create coredns addon: %w", err)
	}

	return nil
}
