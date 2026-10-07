package ekscluster_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"sort"
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/ekscluster"
	"github.com/truvity/k8s/pkg/cluster"
)

// testCAPEM is a throwaway self-signed CA certificate the mocked cluster
// reports (base64-encoded, as EKS does).
var testCAPEM = func() string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kubernetes"}, IsCA: true, BasicConstraintsValid: true}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}()

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

	outs := args.Inputs.Copy()
	if args.TypeToken == "aws:eks/cluster:Cluster" {
		// What EKS reports once the cluster exists.
		outs["endpoint"] = resource.NewStringProperty("https://api.c1.example")
		outs["certificateAuthority"] = resource.NewObjectProperty(resource.PropertyMap{
			"data": resource.NewStringProperty(base64.StdEncoding.EncodeToString([]byte(testCAPEM))),
		})
		outs["identities"] = resource.NewArrayProperty([]resource.PropertyValue{
			resource.NewObjectProperty(resource.PropertyMap{
				"oidcs": resource.NewArrayProperty([]resource.PropertyValue{
					resource.NewObjectProperty(resource.PropertyMap{"issuer": resource.NewStringProperty("https://oidc.example/id/abc")}),
				}),
			}),
		})
	}

	return args.Name + "-id", outs, nil
}

func (*recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func ptr[T any](v T) *T { return &v }

const (
	adminPrincipal  = "arn:example:iam::role/admin"
	viewerPrincipal = "arn:example:iam::role/viewer"
	rewriteLine     = "rewrite name suffix .c1.example. .example. answer auto"

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
		AccessEntries: []ekscluster.AccessEntry{
			{Name: "admin", PrincipalARN: adminPrincipal, PolicyARN: "arn:example:eks::policy/Admin"},
			{Name: "viewer", PrincipalARN: viewerPrincipal, PolicyARN: "arn:example:eks::policy/View", Namespaces: []string{"a", "b"}},
		},
		CoreDNS:        &ekscluster.CoreDNS{Extras: []string{rewriteLine}},
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
	case ekscluster.KindAccessEntry:
		return c.Key + "-access-entry"
	case ekscluster.KindAccessPolicy:
		return c.Key + "-policy"
	case ekscluster.KindCoreDNS:
		return "coredns-addon"
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
		"aws:eks/accessEntry:AccessEntry k-access-admin",
		"aws:eks/accessEntry:AccessEntry k-access-viewer",
		"aws:eks/accessPolicyAssociation:AccessPolicyAssociation k-access-admin-policy",
		"aws:eks/accessPolicyAssociation:AccessPolicyAssociation k-access-viewer-policy",
		"aws:eks/addon:Addon k-coredns",
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
		"aws:eks/accessEntry:AccessEntry admin-access-entry",
		"aws:eks/accessEntry:AccessEntry viewer-access-entry",
		"aws:eks/accessPolicyAssociation:AccessPolicyAssociation admin-policy",
		"aws:eks/accessPolicyAssociation:AccessPolicyAssociation viewer-policy",
		"aws:eks/addon:Addon coredns-addon",
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
		"entry name": {func(a *ekscluster.Args) { a.AccessEntries[0].Name = "" }, "AccessEntries[0].Name is empty"},
		"entry repeat": {func(a *ekscluster.Args) {
			a.AccessEntries[1].Name = "admin"
		}, "AccessEntries[1]: duplicate name"},
		"entry principal": {func(a *ekscluster.Args) { a.AccessEntries[0].PrincipalARN = "" }, "AccessEntries[0].PrincipalARN is empty"},
		"entry principal repeat": {func(a *ekscluster.Args) {
			a.AccessEntries[1].PrincipalARN = adminPrincipal
		}, "already has an entry"},
		"entry policy":   {func(a *ekscluster.Args) { a.AccessEntries[0].PolicyARN = "" }, "AccessEntries[0].PolicyARN is empty"},
		"upgrade policy": {func(a *ekscluster.Args) { a.UpgradePolicy = "standard" }, "UpgradePolicy \"standard\""},
		"unknown capability": {func(a *ekscluster.Args) {
			a.Capabilities = []cluster.Capability{"gpu"}
		}, "unknown capability \"gpu\""},
		"repeated capability": {func(a *ekscluster.Args) {
			a.Capabilities = []cluster.Capability{cluster.Storage, cluster.Storage}
		}, "repeats a capability"},
		"entry namespace": {func(a *ekscluster.Args) { a.AccessEntries[1].Namespaces = []string{""} }, "AccessEntries[1].Namespaces[0] is empty"},
		"coredns version without its corefile": {func(a *ekscluster.Args) {
			a.CoreDNS.Version = "v9.9.9-eksbuild.1"
		}, "needs its stock CoreDNS.Corefile"},
		"coredns no anchor": {func(a *ekscluster.Args) {
			a.CoreDNS.Corefile = ".:53 {\n    errors\n}"
		}, "no \"kubernetes\" stanza"},
		"coredns empty extra":     {func(a *ekscluster.Args) { a.CoreDNS.Extras = []string{" "} }, "CoreDNS.Extras[0] is empty"},
		"coredns multiline extra": {func(a *ekscluster.Args) { a.CoreDNS.Extras = []string{"a\nb"} }, "spans more than one line"},
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

func TestAccessEntries(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for name, principal := range map[string]string{"admin": adminPrincipal, "viewer": viewerPrincipal} {
		e := find(t, res.regs, "aws:eks/accessEntry:AccessEntry", "k-access-"+name)
		if str(e, "clusterName") != "c1" || str(e, "principalArn") != principal || str(e, "type") != "STANDARD" {
			t.Errorf("entry %s: %v", name, e.inputs)
		}

		p := find(t, res.regs, "aws:eks/accessPolicyAssociation:AccessPolicyAssociation", "k-access-"+name+"-policy")
		if str(p, "clusterName") != "c1" || str(p, "principalArn") != principal {
			t.Errorf("policy %s: %v", name, p.inputs)
		}
	}

	admin := find(t, res.regs, "aws:eks/accessPolicyAssociation:AccessPolicyAssociation", "k-access-admin-policy")
	if str(admin, "policyArn") != "arn:example:eks::policy/Admin" {
		t.Errorf("admin policyArn %q", str(admin, "policyArn"))
	}

	if scope := admin.inputs["accessScope"].ObjectValue(); scope["type"].StringValue() != "cluster" || scope.HasValue("namespaces") {
		t.Errorf("admin scope %v, want the cluster", scope)
	}

	viewer := find(t, res.regs, "aws:eks/accessPolicyAssociation:AccessPolicyAssociation", "k-access-viewer-policy")

	scope := viewer.inputs["accessScope"].ObjectValue()
	if scope["type"].StringValue() != "namespace" {
		t.Errorf("viewer scope type %v", scope["type"])
	}

	if got := scope["namespaces"].ArrayValue(); len(got) != 2 || got[0].StringValue() != "a" || got[1].StringValue() != "b" {
		t.Errorf("viewer namespaces %v", got)
	}
}

func TestCoreDNSAddon(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	a := find(t, res.regs, "aws:eks/addon:Addon", "k-coredns")
	if str(a, "clusterName") != "c1" || str(a, "addonName") != "coredns" || str(a, "addonVersion") != ekscluster.DefaultCoreDNSVersion {
		t.Errorf("addon %v", a.inputs)
	}

	if str(a, "resolveConflictsOnCreate") != "OVERWRITE" || str(a, "resolveConflictsOnUpdate") != "OVERWRITE" {
		t.Errorf("conflict resolution %v", a.inputs)
	}

	var values map[string]string
	if err := json.Unmarshal([]byte(str(a, "configurationValues")), &values); err != nil {
		t.Fatalf("configurationValues is not JSON: %v", err)
	}

	corefile := values["corefile"]
	if !strings.Contains(corefile, "    "+rewriteLine+"\n    kubernetes ") {
		t.Fatalf("extra not anchored above the kubernetes stanza:\n%s", corefile)
	}

	// One line added, nothing else touched.
	if got := strings.Replace(corefile, "    "+rewriteLine+"\n", "", 1); got != ekscluster.StockCorefile {
		t.Fatalf("corefile differs from the stock file beyond the extra:\n%s", got)
	}
}

// The rendering is state: a consumer that rendered the configuration itself
// before adopting the component must see the same string, byte for byte.
func TestCoreDNSConfigurationValuesAreStable(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	want := `{"corefile":".:53 {\n    errors\n    health {\n        lameduck 5s\n      }\n    ready\n` +
		`    rewrite name suffix .c1.example. .example. answer auto\n` +
		`    kubernetes cluster.local in-addr.arpa ip6.arpa {\n      pods insecure\n      fallthrough in-addr.arpa ip6.arpa\n    }\n` +
		`    prometheus :9153\n    forward . /etc/resolv.conf\n    cache 30\n    loop\n    reload\n    loadbalance\n}"}`

	if got := str(find(t, res.regs, "aws:eks/addon:Addon", "k-coredns"), "configurationValues"); got != want {
		t.Errorf("configurationValues\n got %s\nwant %s", got, want)
	}
}

func TestCoreDNSExtrasInOrderAndOwnCorefile(t *testing.T) {
	a := base()
	a.CoreDNS = &ekscluster.CoreDNS{
		Version:  "v9.9.9-eksbuild.1",
		Corefile: ".:53 {\n    kubernetes cluster.local\n}",
		Extras:   []string{"one", "two"},
	}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	addon := find(t, res.regs, "aws:eks/addon:Addon", "k-coredns")
	if str(addon, "addonVersion") != "v9.9.9-eksbuild.1" {
		t.Errorf("version %q", str(addon, "addonVersion"))
	}

	if got, want := str(addon, "configurationValues"), `{"corefile":".:53 {\n    one\n    two\n    kubernetes cluster.local\n}"}`; got != want {
		t.Errorf("configurationValues %s, want %s", got, want)
	}
}

func TestNoAccessEntriesAndNoCoreDNSRegisterNeither(t *testing.T) {
	a := base()
	a.AccessEntries, a.CoreDNS = nil, nil

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if strings.HasPrefix(r.typ, "aws:eks/access") || r.typ == "aws:eks/addon:Addon" {
			t.Errorf("registered %s %s", r.typ, r.name)
		}
	}
}

func TestUpgradePolicyAndDeletionProtectionOnlyWhenSet(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	c := find(t, res.regs, "aws:eks/cluster:Cluster", "k-cluster")
	for _, key := range []resource.PropertyKey{"upgradePolicy", "deletionProtection"} {
		if v, ok := c.inputs[key]; ok && !v.IsNull() {
			t.Errorf("%s = %v without being asked for", key, v)
		}
	}

	a := base()
	a.UpgradePolicy = ekscluster.UpgradePolicyStandard
	a.DeletionProtection = ptr(true)

	res, err = run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	c = find(t, res.regs, "aws:eks/cluster:Cluster", "k-cluster")
	if got := c.inputs["upgradePolicy"].ObjectValue()["supportType"].StringValue(); got != "STANDARD" {
		t.Errorf("upgradePolicy.supportType %q", got)
	}

	if v := c.inputs["deletionProtection"]; !v.IsBool() || !v.BoolValue() {
		t.Errorf("deletionProtection = %v, want true", v)
	}
}

// contractOf runs the component and returns what its Contract resolves to.
func contractOf(t *testing.T, a *ekscluster.Args) cluster.Outputs {
	t.Helper()

	var (
		mu  sync.Mutex
		got cluster.Outputs
	)

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "aws-main", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a.Provider = p

		c, err := ekscluster.New(ctx, "k", a)
		if err != nil {
			return err
		}

		c.Contract.ApplyT(func(o cluster.Outputs) error {
			mu.Lock()
			got = o
			mu.Unlock()

			return nil
		})

		return nil
	}, pulumi.WithMocks("example", "dev", &recorder{}))
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	return got
}

func TestContractReportsTheClusterUnderTheProviderNeutralContract(t *testing.T) {
	got := contractOf(t, base())

	if err := got.Validate(); err != nil {
		t.Fatalf("the contract does not validate: %v", err)
	}

	if got.Provider != cluster.ProviderEKS || got.Name != "c1" || got.Endpoint != "https://api.c1.example" {
		t.Errorf("provider, name, endpoint: %q %q %q", got.Provider, got.Name, got.Endpoint)
	}

	if got.CertificateAuthorityPEM != testCAPEM {
		t.Errorf("the certificate authority is not the decoded PEM:\n%s", got.CertificateAuthorityPEM)
	}

	if got.OIDCIssuer != "https://oidc.example/id/abc" {
		t.Errorf("oidc issuer %q", got.OIDCIssuer)
	}

	var caps []string
	for _, c := range got.Capabilities.List() {
		caps = append(caps, string(c))
	}

	if want := "node-pools,workload-identity,load-balancing,api-access"; strings.Join(caps, ",") != want {
		t.Errorf("capabilities %v, want %s", caps, want)
	}
}

func TestContractCapabilitiesAreTheCallers(t *testing.T) {
	a := base()
	a.Capabilities = []cluster.Capability{cluster.NodePools, cluster.Storage, cluster.NetworkPolicy}

	got := contractOf(t, a)
	if err := got.Validate(); err != nil {
		t.Fatalf("the contract does not validate: %v", err)
	}

	if !got.Capabilities.Has(cluster.Storage) || got.Capabilities.Has(cluster.WorkloadIdentity) {
		t.Errorf("capabilities %v", got.Capabilities.List())
	}

	if got.OIDCIssuer != "" {
		t.Errorf("issuer %q reported without workload-identity", got.OIDCIssuer)
	}
}
