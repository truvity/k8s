package ekscluster_test

import (
	"sort"
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/ekscluster"
)

type alias struct {
	typ      string
	noParent bool
}

type registration struct {
	typ, name, parent, provider string
	aliases                     []alias
	protect                     bool
	inputs                      resource.PropertyMap
}

type recorder struct {
	mu   sync.Mutex
	regs []registration
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	reg := registration{typ: args.TypeToken, name: args.Name, inputs: args.Inputs}

	if rpc := args.RegisterRPC; rpc != nil {
		reg.parent = rpc.GetParent()
		reg.provider = rpc.GetProvider()
		reg.protect = rpc.GetProtect()

		for _, a := range rpc.GetAliases() {
			if spec := a.GetSpec(); spec != nil && spec.GetName() == "" {
				reg.aliases = append(reg.aliases, alias{typ: spec.GetType(), noParent: spec.GetNoParent()})
			}
		}
	}

	r.mu.Lock()
	r.regs = append(r.regs, reg)
	r.mu.Unlock()

	return args.Name + "-id", args.Inputs.Copy(), nil
}

func (*recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func ptr[T any](v T) *T { return &v }

const (
	clusterPolicyA = "arn:example:iam::policy/ClusterPolicyA"
	clusterPolicyB = "arn:example:iam::policy/service-role/ClusterPolicyB"
	nodePolicy     = "arn:example:iam::policy/NodePolicy"
)

// base builds args for a small cluster; a test changes what it is about. The
// provider is filled in by run.
func base() *ekscluster.Args {
	return &ekscluster.Args{
		Name:                "c1",
		Version:             "1.99",
		SubnetIDs:           []string{"subnet-a", "subnet-b"},
		ServiceCIDR:         "172.20.0.0/16",
		PermissionsBoundary: "boundary-arn",
		ClusterPolicies:     []string{clusterPolicyA, clusterPolicyB},
		NodePolicies:        []string{nodePolicy},
		NodeInlinePolicies: []ekscluster.InlinePolicy{
			{Name: "pull", Document: pulumi.String(`{"pull":true}`)},
			{Name: "import", Document: pulumi.String(`{"import":true}`)},
		},
		LegacyTopLevel: true,
	}
}

// legacy names the children the way a stack that predates the component
// might have.
func legacy(c ekscluster.Child) string {
	switch c.Kind {
	case ekscluster.KindKey:
		return "secrets-key"
	case ekscluster.KindKeyAlias:
		return "secrets-key-alias"
	case ekscluster.KindClusterRole:
		return "cluster-role"
	case ekscluster.KindClusterPolicy:
		return "cluster-" + c.Key
	case ekscluster.KindNodeRole:
		return "node-role"
	case ekscluster.KindNodePolicy:
		return "node-" + c.Key
	case ekscluster.KindNodeInlinePolicy:
		return "node-inline-" + c.Key
	case ekscluster.KindLogGroup:
		return "cluster-logs"
	default:
		return "cluster"
	}
}

type result struct {
	regs []registration
	comp *ekscluster.EksCluster
}

func run(t *testing.T, args *ekscluster.Args, mutate ...func(a *ekscluster.Args)) (result, error) {
	t.Helper()

	rec := &recorder{}

	var res result

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "aws-main", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		args.Provider = p

		for _, m := range mutate {
			m(args)
		}

		res.comp, err = ekscluster.New(ctx, "k", args)

		return err
	}, pulumi.WithMocks("example", "dev", rec))

	res.regs = rec.regs

	return res, err
}

func children(regs []registration) []registration {
	var out []registration

	for _, r := range regs {
		if r.typ != ekscluster.TypeToken && !strings.HasPrefix(r.typ, "pulumi:providers:") {
			out = append(out, r)
		}
	}

	return out
}

func find(t *testing.T, regs []registration, typ, name string) registration {
	t.Helper()

	for _, r := range regs {
		if r.typ == typ && r.name == name {
			return r
		}
	}

	t.Fatalf("no %s %s registered", typ, name)

	return registration{}
}

func names(regs []registration) []string {
	var got []string
	for _, r := range children(regs) {
		got = append(got, r.typ+" "+r.name)
	}

	sort.Strings(got)

	return got
}

func str(r registration, key string) string {
	if v, ok := r.inputs[resource.PropertyKey(key)]; ok && v.IsString() {
		return v.StringValue()
	}

	return ""
}

func strs(r registration, key string) []string {
	var out []string

	if v, ok := r.inputs[resource.PropertyKey(key)]; ok && v.IsArray() {
		for _, e := range v.ArrayValue() {
			out = append(out, e.StringValue())
		}
	}

	return out
}

func TestDefaultNames(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:cloudwatch/logGroup:LogGroup k-logs",
		"aws:eks/cluster:Cluster k-cluster",
		"aws:iam/role:Role k-cluster-role",
		"aws:iam/role:Role k-node-role",
		"aws:iam/rolePolicy:RolePolicy k-node-role-policy-import",
		"aws:iam/rolePolicy:RolePolicy k-node-role-policy-pull",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment k-cluster-role-ClusterPolicyA",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment k-cluster-role-ClusterPolicyB",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment k-node-role-NodePolicy",
		"aws:kms/alias:Alias k-key-alias",
		"aws:kms/key:Key k-key",
	}

	if got := names(res.regs); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("children:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestChildrenKeepTheNamesTheHookGives(t *testing.T) {
	a := base()
	a.Names = legacy

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:cloudwatch/logGroup:LogGroup cluster-logs",
		"aws:eks/cluster:Cluster cluster",
		"aws:iam/role:Role cluster-role",
		"aws:iam/role:Role node-role",
		"aws:iam/rolePolicy:RolePolicy node-inline-import",
		"aws:iam/rolePolicy:RolePolicy node-inline-pull",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment cluster-ClusterPolicyA",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment cluster-ClusterPolicyB",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment node-NodePolicy",
		"aws:kms/alias:Alias secrets-key-alias",
		"aws:kms/key:Key secrets-key",
	}

	if got := names(res.regs); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("children:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestEveryChildIsUnderTheComponentAndItsProvider(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	var comp, prov string

	for _, r := range res.regs {
		if r.typ == ekscluster.TypeToken {
			comp = r.name
		}

		if strings.HasPrefix(r.typ, "pulumi:providers:") {
			prov = r.name
		}
	}

	if comp == "" {
		t.Fatal("component not registered")
	}

	for _, r := range children(res.regs) {
		if !strings.HasSuffix(r.parent, "::"+comp) || !strings.Contains(r.parent, ekscluster.TypeToken) {
			t.Errorf("%s %s: parent %q is not the component", r.typ, r.name, r.parent)
		}

		if !strings.Contains(r.provider, prov) {
			t.Errorf("%s %s: provider %q is not the caller's", r.typ, r.name, r.provider)
		}
	}
}

func TestAliasesAreNoParentOnEveryChildWithLegacyTopLevel(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		var plain int

		for _, al := range r.aliases {
			if al.noParent && al.typ == "" {
				plain++
			}
		}

		if plain != 1 {
			t.Errorf("%s %s: want exactly one plain NoParent alias, got %d", r.typ, r.name, plain)
		}
	}
}

// The AWS SDK may declare aliases of its own on these types without a parent.
// Under a component those resolve beneath the component, so a state still
// holding the former type at the top level would be missed. ADR 0003 item 6:
// whatever the SDK aliases, the legacy set must repeat. The SDK declares none
// on these types today; this goes red when an upgrade adds one.
func TestTheSDKAliasesNoneOfTheseTypes(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		for _, al := range r.aliases {
			if !al.noParent {
				t.Errorf("%s %s: the SDK now aliases from %q; make LegacyTopLevel repeat it with no parent (ADR 0003 item 6)", r.typ, r.name, al.typ)
			}

			if al.noParent && al.typ != "" {
				t.Errorf("%s %s: a typed NoParent alias from %q the SDK does not declare", r.typ, r.name, al.typ)
			}
		}
	}
}

func TestWithoutLegacyTopLevelNoChildCarriesAnAlias(t *testing.T) {
	a := base()
	a.LegacyTopLevel = false

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if len(r.aliases) != 0 {
			t.Errorf("%s %s carries an alias without LegacyTopLevel", r.typ, r.name)
		}
	}
}

func TestProtectsTheClusterTheKeyAndTheRolesByDefaultAndOnlyThem(t *testing.T) {
	for name, tc := range map[string]struct {
		protect *bool
		want    bool
	}{"nil": {nil, true}, "true": {ptr(true), true}, "false": {ptr(false), false}} {
		t.Run(name, func(t *testing.T) {
			a := base()
			a.Protect = tc.protect

			res, err := run(t, a)
			if err != nil {
				t.Fatal(err)
			}

			var protected int

			for _, r := range children(res.regs) {
				core := r.typ == "aws:eks/cluster:Cluster" || r.typ == "aws:kms/key:Key" || r.typ == "aws:iam/role:Role"
				if want := core && tc.want; r.protect != want {
					t.Errorf("%s %s: protect = %v, want %v", r.typ, r.name, r.protect, want)
				}

				if r.protect {
					protected++
				}
			}

			if tc.want && protected != 4 { // cluster, key, cluster role, node role
				t.Errorf("%d protected children, want 4", protected)
			}
		})
	}
}

func TestClusterInputs(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	c := find(t, res.regs, "aws:eks/cluster:Cluster", "k-cluster")

	if got := str(c, "name"); got != "c1" {
		t.Errorf("name %q", got)
	}

	if got := str(c, "version"); got != "1.99" {
		t.Errorf("version %q", got)
	}

	if got := strings.Join(strs(c, "enabledClusterLogTypes"), ","); got != "api,audit,authenticator,controllerManager,scheduler" {
		t.Errorf("log types %q", got)
	}

	if v := c.inputs["bootstrapSelfManagedAddons"]; !v.IsBool() || v.BoolValue() {
		t.Errorf("bootstrapSelfManagedAddons = %v, want false", v)
	}

	vpc := c.inputs["vpcConfig"].ObjectValue()
	if !vpc["endpointPrivateAccess"].BoolValue() || vpc["endpointPublicAccess"].BoolValue() {
		t.Errorf("endpoint access %v, want private only", vpc)
	}

	if got := vpc["subnetIds"].ArrayValue(); len(got) != 2 || got[0].StringValue() != "subnet-a" {
		t.Errorf("subnetIds %v", got)
	}

	ac := c.inputs["accessConfig"].ObjectValue()
	if ac["authenticationMode"].StringValue() != "API" || ac["bootstrapClusterCreatorAdminPermissions"].BoolValue() {
		t.Errorf("accessConfig %v", ac)
	}

	cc := c.inputs["computeConfig"].ObjectValue()
	if !cc["enabled"].BoolValue() || len(cc["nodePools"].ArrayValue()) != 2 {
		t.Errorf("computeConfig %v", cc)
	}

	kn := c.inputs["kubernetesNetworkConfig"].ObjectValue()
	if kn["serviceIpv4Cidr"].StringValue() != "172.20.0.0/16" || !kn["elasticLoadBalancing"].ObjectValue()["enabled"].BoolValue() {
		t.Errorf("kubernetesNetworkConfig %v", kn)
	}

	if !c.inputs["storageConfig"].ObjectValue()["blockStorage"].ObjectValue()["enabled"].BoolValue() {
		t.Error("block storage is not enabled")
	}

	enc := c.inputs["encryptionConfig"].ObjectValue()
	if got := enc["resources"].ArrayValue(); len(got) != 1 || got[0].StringValue() != "secrets" {
		t.Errorf("encrypted resources %v", got)
	}

	if _, ok := c.inputs["tags"]; ok {
		t.Error("tags set without Args.Tags")
	}
}

func TestOverridesReachTheCluster(t *testing.T) {
	a := base()
	a.PublicEndpoint = true
	a.NodePools = []string{"system"}
	a.LogTypes = []string{"api"}
	a.Tags = map[string]string{"Team": "x"}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	c := find(t, res.regs, "aws:eks/cluster:Cluster", "k-cluster")

	if !c.inputs["vpcConfig"].ObjectValue()["endpointPublicAccess"].BoolValue() {
		t.Error("PublicEndpoint did not open the endpoint")
	}

	if got := c.inputs["computeConfig"].ObjectValue()["nodePools"].ArrayValue(); len(got) != 1 {
		t.Errorf("nodePools %v", got)
	}

	if got := strings.Join(strs(c, "enabledClusterLogTypes"), ","); got != "api" {
		t.Errorf("log types %q", got)
	}

	for _, r := range children(res.regs) {
		if r.typ == "aws:eks/cluster:Cluster" || r.typ == "aws:kms/key:Key" || r.typ == "aws:iam/role:Role" || r.typ == "aws:cloudwatch/logGroup:LogGroup" {
			if got := r.inputs["tags"].ObjectValue()["Team"]; got.StringValue() != "x" {
				t.Errorf("%s %s: tags %v", r.typ, r.name, r.inputs["tags"])
			}
		}
	}
}

func TestKeyRolesLogsAndDefaults(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	k := find(t, res.regs, "aws:kms/key:Key", "k-key")
	if got := str(k, "description"); got != "EKS c1 cluster secrets encryption" {
		t.Errorf("key description %q", got)
	}

	if !k.inputs["enableKeyRotation"].BoolValue() || k.inputs["rotationPeriodInDays"].NumberValue() != 365 {
		t.Errorf("rotation %v %v", k.inputs["enableKeyRotation"], k.inputs["rotationPeriodInDays"])
	}

	if got := str(find(t, res.regs, "aws:kms/alias:Alias", "k-key-alias"), "name"); got != "alias/eks-c1-secrets" {
		t.Errorf("alias %q", got)
	}

	lg := find(t, res.regs, "aws:cloudwatch/logGroup:LogGroup", "k-logs")
	if str(lg, "name") != "/aws/eks/c1/cluster" || lg.inputs["retentionInDays"].NumberValue() != 90 {
		t.Errorf("log group %v", lg.inputs)
	}

	cr := find(t, res.regs, "aws:iam/role:Role", "k-cluster-role")
	if str(cr, "name") != "c1-cluster" || str(cr, "permissionsBoundary") != "boundary-arn" {
		t.Errorf("cluster role %v", cr.inputs)
	}

	if !strings.Contains(str(cr, "assumeRolePolicy"), "eks.amazonaws.com") || !strings.Contains(str(cr, "assumeRolePolicy"), "sts:TagSession") {
		t.Errorf("cluster trust %q", str(cr, "assumeRolePolicy"))
	}

	nr := find(t, res.regs, "aws:iam/role:Role", "k-node-role")
	if str(nr, "name") != "c1-node" || !strings.Contains(str(nr, "assumeRolePolicy"), "ec2.amazonaws.com") ||
		strings.Contains(str(nr, "assumeRolePolicy"), "TagSession") {
		t.Errorf("node role %v", nr.inputs)
	}

	pol := find(t, res.regs, "aws:iam/rolePolicy:RolePolicy", "k-node-role-policy-pull")
	if str(pol, "name") != "pull" || str(pol, "policy") != `{"pull":true}` {
		t.Errorf("inline policy %v", pol.inputs)
	}

	att := find(t, res.regs, "aws:iam/rolePolicyAttachment:RolePolicyAttachment", "k-cluster-role-ClusterPolicyB")
	if str(att, "policyArn") != clusterPolicyB {
		t.Errorf("attachment %v", att.inputs)
	}
}

func TestExplicitValuesWinOverDefaults(t *testing.T) {
	a := base()
	a.ClusterRoleName, a.NodeRoleName = "cr", "nr"
	a.KeyDescription, a.KeyAlias, a.KeyRotationDays = "d", "alias/x", 180
	a.LogGroupName, a.LogRetentionDays = "/lg", 30
	a.PermissionsBoundary = ""
	a.ClusterTrustPolicy, a.NodeTrustPolicy = pulumi.String(`{"c":1}`), pulumi.String(`{"n":1}`)

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	cr := find(t, res.regs, "aws:iam/role:Role", "k-cluster-role")
	nr := find(t, res.regs, "aws:iam/role:Role", "k-node-role")

	if str(cr, "name") != "cr" || str(nr, "name") != "nr" {
		t.Errorf("role names %q %q", str(cr, "name"), str(nr, "name"))
	}

	if str(cr, "assumeRolePolicy") != `{"c":1}` || str(nr, "assumeRolePolicy") != `{"n":1}` {
		t.Errorf("trust is not verbatim: %q %q", str(cr, "assumeRolePolicy"), str(nr, "assumeRolePolicy"))
	}

	if _, ok := cr.inputs["permissionsBoundary"]; ok {
		t.Error("boundary set though empty")
	}

	k := find(t, res.regs, "aws:kms/key:Key", "k-key")
	if str(k, "description") != "d" || k.inputs["rotationPeriodInDays"].NumberValue() != 180 {
		t.Errorf("key %v", k.inputs)
	}

	if str(find(t, res.regs, "aws:kms/alias:Alias", "k-key-alias"), "name") != "alias/x" {
		t.Error("alias name")
	}

	lg := find(t, res.regs, "aws:cloudwatch/logGroup:LogGroup", "k-logs")
	if str(lg, "name") != "/lg" || lg.inputs["retentionInDays"].NumberValue() != 30 {
		t.Errorf("log group %v", lg.inputs)
	}
}

func TestRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(a *ekscluster.Args)
		want   string
	}{
		"name":          {func(a *ekscluster.Args) { a.Name = "" }, "Name is empty"},
		"version":       {func(a *ekscluster.Args) { a.Version = "" }, "Version is empty"},
		"no subnets":    {func(a *ekscluster.Args) { a.SubnetIDs = nil }, "SubnetIDs is empty"},
		"empty subnet":  {func(a *ekscluster.Args) { a.SubnetIDs = []string{""} }, "SubnetIDs[0] is empty"},
		"dup subnet":    {func(a *ekscluster.Args) { a.SubnetIDs = []string{"s", "s"} }, "duplicate"},
		"no cidr":       {func(a *ekscluster.Args) { a.ServiceCIDR = "" }, "ServiceCIDR is empty"},
		"bad cidr":      {func(a *ekscluster.Args) { a.ServiceCIDR = "nope" }, "ServiceCIDR"},
		"ipv6 cidr":     {func(a *ekscluster.Args) { a.ServiceCIDR = "2001:db8::/32" }, "not IPv4"},
		"host bits":     {func(a *ekscluster.Args) { a.ServiceCIDR = "172.20.0.1/16" }, "host bits"},
		"no policies":   {func(a *ekscluster.Args) { a.ClusterPolicies = nil }, "ClusterPolicies is empty"},
		"empty policy":  {func(a *ekscluster.Args) { a.NodePolicies = []string{""} }, "NodePolicies[0] is empty"},
		"dup policy":    {func(a *ekscluster.Args) { a.ClusterPolicies = []string{"p/a", "p/a"} }, "duplicate"},
		"inline name":   {func(a *ekscluster.Args) { a.NodeInlinePolicies[0].Name = "" }, "NodeInlinePolicies[0].Name"},
		"inline doc":    {func(a *ekscluster.Args) { a.NodeInlinePolicies[0].Document = nil }, "NodeInlinePolicies[0].Document"},
		"inline repeat": {func(a *ekscluster.Args) { a.NodeInlinePolicies[1].Name = "pull" }, "duplicate name"},
		"same roles": {func(a *ekscluster.Args) {
			a.ClusterRoleName, a.NodeRoleName = "r", "r"
		}, "both named"},
		"log type":   {func(a *ekscluster.Args) { a.LogTypes = []string{"everything"} }, "unknown log type"},
		"empty pool": {func(a *ekscluster.Args) { a.NodePools = []string{""} }, "NodePools[0] is empty"},
		"rotation":   {func(a *ekscluster.Args) { a.KeyRotationDays = 10 }, "KeyRotationDays"},
		"retention":  {func(a *ekscluster.Args) { a.LogRetentionDays = -1 }, "LogRetentionDays"},
		"empty name": {func(a *ekscluster.Args) {
			a.Names = func(c ekscluster.Child) string {
				if c.Kind == ekscluster.KindKey {
					return ""
				}

				return ekscluster.DefaultName(c)
			}
		}, "empty name for key"},
		"repeated name": {func(a *ekscluster.Args) {
			a.Names = func(ekscluster.Child) string { return "same" }
		}, "for both"},
	} {
		t.Run(name, func(t *testing.T) {
			a := base()
			tc.mutate(a)

			res, err := run(t, a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}

			if len(children(res.regs)) != 0 {
				t.Errorf("registered %v before refusing", names(res.regs))
			}
		})
	}
}

func TestMissingProviderIsRefused(t *testing.T) {
	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := ekscluster.New(ctx, "k", base())
		return err
	}, pulumi.WithMocks("example", "dev", rec))

	if err == nil || !strings.Contains(err.Error(), "Provider is nil") {
		t.Fatalf("err = %v", err)
	}

	if len(rec.regs) != 0 {
		t.Errorf("registered %d resources before refusing", len(rec.regs))
	}
}

func TestRefusalReportsEveryProblemAtOnce(t *testing.T) {
	a := base()
	a.Name, a.Version, a.SubnetIDs, a.ServiceCIDR, a.ClusterPolicies = "", "", nil, "", nil

	_, err := run(t, a)
	if err == nil {
		t.Fatal("want an error")
	}

	for _, want := range []string{"Name is empty", "Version is empty", "SubnetIDs is empty", "ServiceCIDR is empty", "ClusterPolicies is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestOutputsExist(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	c := res.comp
	for name, out := range map[string]pulumi.StringOutput{
		"KeyARN": c.KeyARN, "ClusterRoleARN": c.ClusterRoleARN, "ClusterRoleName": c.ClusterRoleName,
		"NodeRoleARN": c.NodeRoleARN, "NodeRoleName": c.NodeRoleName,
	} {
		if out == (pulumi.StringOutput{}) {
			t.Errorf("%s is not set", name)
		}
	}

	if c.Cluster == nil {
		t.Error("Cluster is not set")
	}
}
