package podidentity_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/podidentity"
)

type alias struct {
	typ      string
	noParent bool
}

type registration struct {
	typ, name, parent, provider, importID string
	aliases                               []alias
	ignore                                []string
	protect                               bool
	inputs                                resource.PropertyMap
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
		reg.importID = rpc.GetImportId()
		reg.ignore = rpc.GetIgnoreChanges()

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

const (
	account = "acct-example"
	cluster = "arn-of-the-cluster"
)

// base builds args with every input a test may need; a test removes or adds
// what it is about. The provider is filled in by run.
func base() *podidentity.Args {
	return &podidentity.Args{
		ClusterName:         pulumi.String("c1"),
		Region:              "eu-central-1",
		Namespace:           "team-a",
		ServiceAccounts:     []string{"app"},
		RoleName:            "c1-app",
		PermissionsBoundary: "boundary-arn",
		AccountID:           account,
		ClusterARN:          cluster,
		InlinePolicy:        &podidentity.InlinePolicy{Name: "inline", Document: pulumi.String(`{"inline":true}`)},
		ManagedPolicy: &podidentity.ManagedPolicy{
			Name: "c1-app", Description: "what it is for", Document: pulumi.String(`{"managed":true}`),
		},
		LegacyTopLevel: true,
	}
}

// legacy reproduces the names a stack used before it became a component.
func legacy(c podidentity.Child) string {
	switch c.Kind {
	case podidentity.KindPolicy:
		return "svc/c1-app/policy"
	case podidentity.KindAttachment:
		return "svc/c1-app/attachment"
	case podidentity.KindRolePolicy:
		return "svc/c1-app/inline"
	case podidentity.KindAssociation:
		return "svc/c1-app/pia-" + c.Key
	default:
		return "svc/c1-app/role"
	}
}

type result struct{ regs []registration }

func run(t *testing.T, args *podidentity.Args) (result, error) {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "aws", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		args.Provider = p
		_, err = podidentity.New(ctx, "ident", args)

		return err
	}, pulumi.WithMocks("example", "dev", rec))

	return result{regs: rec.regs}, err
}

func children(regs []registration) []registration {
	var out []registration

	for i := range regs {
		if r := &regs[i]; r.typ != podidentity.TypeToken && !strings.HasPrefix(r.typ, "pulumi:providers:") {
			out = append(out, *r)
		}
	}

	return out
}

func find(t *testing.T, regs []registration, typ, name string) registration {
	t.Helper()

	for i := range regs {
		if regs[i].typ == typ && regs[i].name == name {
			return regs[i]
		}
	}

	t.Fatalf("no %s %s registered", typ, name)

	return registration{}
}

func names(regs []registration) []string {
	var got []string

	kids := children(regs)
	for i := range kids {
		got = append(got, kids[i].typ+" "+kids[i].name)
	}

	sort.Strings(got)

	return got
}

func str(r registration, key string) string {
	return r.inputs[resource.PropertyKey(key)].StringValue()
}

func TestDefaultNames(t *testing.T) {
	a := base()
	a.ServiceAccounts = []string{"app", "job"}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:eks/podIdentityAssociation:PodIdentityAssociation ident-pia-app",
		"aws:eks/podIdentityAssociation:PodIdentityAssociation ident-pia-job",
		"aws:iam/policy:Policy ident-policy",
		"aws:iam/role:Role ident-role",
		"aws:iam/rolePolicy:RolePolicy ident-role-policy",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment ident-attachment",
	}

	if got := names(res.regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
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
		"aws:eks/podIdentityAssociation:PodIdentityAssociation svc/c1-app/pia-app",
		"aws:iam/policy:Policy svc/c1-app/policy",
		"aws:iam/role:Role svc/c1-app/role",
		"aws:iam/rolePolicy:RolePolicy svc/c1-app/inline",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment svc/c1-app/attachment",
	}

	if got := names(res.regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestPermissionsAreOptional(t *testing.T) {
	a := base()
	a.InlinePolicy, a.ManagedPolicy = nil, nil

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:eks/podIdentityAssociation:PodIdentityAssociation ident-pia-app",
		"aws:iam/role:Role ident-role",
	}

	if got := names(res.regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestEveryChildIsUnderTheComponentAndItsProvider(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	want := "::" + podidentity.TypeToken + "::ident"

	for _, r := range children(res.regs) {
		if !strings.Contains(r.parent, want) {
			t.Errorf("%s %s: parent %q is not the component", r.typ, r.name, r.parent)
		}

		if !strings.Contains(r.provider, "pulumi:providers:aws::aws") {
			t.Errorf("%s %s: provider %q is not the one in Args", r.typ, r.name, r.provider)
		}
	}
}

func TestAliasesAreNoParentOnEveryChildWithLegacyTopLevel(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		plain := 0

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
			t.Errorf("%s %s: unexpected aliases %v", r.typ, r.name, r.aliases)
		}
	}
}

func TestNothingIsProtectedByDefault(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if r.protect {
			t.Errorf("%s %s is protected by default", r.typ, r.name)
		}
	}
}

func TestProtectCoversTheRolePolicyAndAssociationsOnly(t *testing.T) {
	a := base()
	a.Protect = true

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		want := !strings.Contains(r.typ, "rolePolicy")
		if r.protect != want {
			t.Errorf("%s %s: protect = %v, want %v", r.typ, r.name, r.protect, want)
		}
	}
}

func TestDefaultTrustIsScopedToTheClusterNamespaceAndServiceAccount(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	role := find(t, res.regs, "aws:iam/role:Role", "ident-role")

	var doc struct {
		Statement []struct {
			Effect    string
			Principal map[string]string
			Action    []string
			Condition map[string]map[string]any
		}
	}

	if err := json.Unmarshal([]byte(str(role, "assumeRolePolicy")), &doc); err != nil {
		t.Fatal(err)
	}

	if len(doc.Statement) != 1 {
		t.Fatalf("want one statement, got %d", len(doc.Statement))
	}

	s := doc.Statement[0]
	if s.Effect != "Allow" || s.Principal["Service"] != "pods.eks.amazonaws.com" ||
		!slices.Equal(s.Action, []string{"sts:AssumeRole", "sts:TagSession"}) {
		t.Errorf("statement = %+v", s)
	}

	eq := s.Condition["StringEquals"]
	if eq["aws:SourceAccount"] != account ||
		eq["aws:RequestTag/kubernetes-namespace"] != "team-a" ||
		eq["aws:RequestTag/kubernetes-service-account"] != "app" {
		t.Errorf("StringEquals = %v", eq)
	}

	if s.Condition["ArnEquals"]["aws:SourceArn"] != cluster {
		t.Errorf("ArnEquals = %v", s.Condition["ArnEquals"])
	}
}

func TestSeveralServiceAccountsAreAListInOrder(t *testing.T) {
	a := base()
	a.ServiceAccounts = []string{"zeta", "alpha"}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	role := find(t, res.regs, "aws:iam/role:Role", "ident-role")
	if got := str(role, "assumeRolePolicy"); !strings.Contains(got, `"aws:RequestTag/kubernetes-service-account":["zeta","alpha"]`) {
		t.Errorf("trust = %s", got)
	}
}

func TestTrustPolicyOverrideIsUsedVerbatim(t *testing.T) {
	a := base()
	a.TrustPolicy = pulumi.String("  {\"verbatim\": 1}\n")
	a.AccountID, a.ClusterARN = "", ""

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	role := find(t, res.regs, "aws:iam/role:Role", "ident-role")
	if got := str(role, "assumeRolePolicy"); got != "  {\"verbatim\": 1}\n" {
		t.Errorf("trust = %q", got)
	}
}

func TestInputs(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	role := find(t, res.regs, "aws:iam/role:Role", "ident-role")
	if str(role, "name") != "c1-app" || str(role, "permissionsBoundary") != "boundary-arn" {
		t.Errorf("role inputs = %v", role.inputs)
	}

	pol := find(t, res.regs, "aws:iam/policy:Policy", "ident-policy")
	if str(pol, "name") != "c1-app" || str(pol, "description") != "what it is for" || str(pol, "policy") != `{"managed":true}` {
		t.Errorf("policy inputs = %v", pol.inputs)
	}

	inl := find(t, res.regs, "aws:iam/rolePolicy:RolePolicy", "ident-role-policy")
	if str(inl, "name") != "inline" || str(inl, "policy") != `{"inline":true}` {
		t.Errorf("inline inputs = %v", inl.inputs)
	}

	pia := find(t, res.regs, "aws:eks/podIdentityAssociation:PodIdentityAssociation", "ident-pia-app")
	if str(pia, "clusterName") != "c1" || str(pia, "namespace") != "team-a" ||
		str(pia, "serviceAccount") != "app" || str(pia, "region") != "eu-central-1" {
		t.Errorf("association inputs = %v", pia.inputs)
	}
}

func TestAssociationRegionAndBoundaryAreUnsetWhenEmpty(t *testing.T) {
	a := base()
	a.Region, a.PermissionsBoundary = "", ""

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	pia := find(t, res.regs, "aws:eks/podIdentityAssociation:PodIdentityAssociation", "ident-pia-app")
	if _, ok := pia.inputs["region"]; ok {
		t.Errorf("region is set: %v", pia.inputs["region"])
	}

	role := find(t, res.regs, "aws:iam/role:Role", "ident-role")
	if _, ok := role.inputs["permissionsBoundary"]; ok {
		t.Errorf("permissionsBoundary is set: %v", role.inputs["permissionsBoundary"])
	}
}

func TestImportsAndIgnoredTags(t *testing.T) {
	a := base()
	a.IgnoreAssociationTags = true
	a.Imports = podidentity.Imports{
		Role: "c1-app", Policy: "policy-arn", Attachment: "c1-app/policy-arn",
		Associations: map[string]string{"app": "c1,a-123"},
	}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for typ, want := range map[string]string{
		"aws:iam/role:Role": "c1-app", "aws:iam/policy:Policy": "policy-arn",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment":     "c1-app/policy-arn",
		"aws:eks/podIdentityAssociation:PodIdentityAssociation": "c1,a-123",
	} {
		for _, r := range children(res.regs) {
			if r.typ == typ && r.importID != want {
				t.Errorf("%s %s: import %q, want %q", r.typ, r.name, r.importID, want)
			}
		}
	}

	pia := find(t, res.regs, "aws:eks/podIdentityAssociation:PodIdentityAssociation", "ident-pia-app")
	if !slices.Equal(pia.ignore, []string{"tags", "tagsAll"}) {
		t.Errorf("ignoreChanges = %v", pia.ignore)
	}

	inl := find(t, res.regs, "aws:iam/rolePolicy:RolePolicy", "ident-role-policy")
	if inl.importID != "" || len(inl.ignore) != 0 {
		t.Errorf("inline policy carries import %q / ignore %v", inl.importID, inl.ignore)
	}
}

func TestNothingIsImportedOrIgnoredByDefault(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if r.importID != "" || len(r.ignore) != 0 {
			t.Errorf("%s %s: import %q ignore %v", r.typ, r.name, r.importID, r.ignore)
		}
	}
}

func TestRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(a *podidentity.Args)
		want   string
	}{
		"cluster":         {func(a *podidentity.Args) { a.ClusterName = nil }, "ClusterName is nil"},
		"role name":       {func(a *podidentity.Args) { a.RoleName = "" }, "RoleName is empty"},
		"namespace":       {func(a *podidentity.Args) { a.Namespace = "" }, "Namespace is empty"},
		"no accounts":     {func(a *podidentity.Args) { a.ServiceAccounts = nil }, "ServiceAccounts is empty"},
		"empty account":   {func(a *podidentity.Args) { a.ServiceAccounts = []string{""} }, "ServiceAccounts[0] is empty"},
		"repeated":        {func(a *podidentity.Args) { a.ServiceAccounts = []string{"a", "a"} }, "duplicate"},
		"no account id":   {func(a *podidentity.Args) { a.AccountID = "" }, "AccountID is empty"},
		"no cluster arn":  {func(a *podidentity.Args) { a.ClusterARN = "" }, "ClusterARN is empty"},
		"inline name":     {func(a *podidentity.Args) { a.InlinePolicy.Name = "" }, "InlinePolicy.Name"},
		"inline document": {func(a *podidentity.Args) { a.InlinePolicy.Document = nil }, "InlinePolicy.Document"},
		"managed name":    {func(a *podidentity.Args) { a.ManagedPolicy.Name = "" }, "ManagedPolicy.Name"},
		"managed doc":     {func(a *podidentity.Args) { a.ManagedPolicy.Document = nil }, "ManagedPolicy.Document"},
		"stray import": {func(a *podidentity.Args) {
			a.Imports.Associations = map[string]string{"nope": "x"}
		}, `"nope"`},
		"empty name": {func(a *podidentity.Args) {
			a.Names = func(c podidentity.Child) string {
				if c.Kind == podidentity.KindRole {
					return ""
				}

				return podidentity.DefaultName(c)
			}
		}, "empty name for role"},
		"repeated name": {func(a *podidentity.Args) {
			a.Names = func(podidentity.Child) string { return "same" }
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
		_, err := podidentity.New(ctx, "ident", base())
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
	a.RoleName, a.Namespace, a.ServiceAccounts = "", "", nil
	a.AccountID = ""

	_, err := run(t, a)
	if err == nil {
		t.Fatal("want an error")
	}

	for _, want := range []string{"RoleName is empty", "Namespace is empty", "ServiceAccounts is empty", "AccountID is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestRoleOutputsExist(t *testing.T) {
	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "aws", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a := base()
		a.Provider = p

		c, err := podidentity.New(ctx, "ident", a)
		if err != nil {
			return err
		}

		if c.RoleARN == (pulumi.StringOutput{}) || c.RoleName == (pulumi.StringOutput{}) {
			return fmt.Errorf("role outputs are not set")
		}

		return nil
	}, pulumi.WithMocks("example", "dev", rec))
	if err != nil {
		t.Fatal(err)
	}
}
