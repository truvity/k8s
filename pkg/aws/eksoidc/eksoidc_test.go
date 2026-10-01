package eksoidc_test

import (
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/eksoidc"
)

const configType = "aws:eks/identityProviderConfig:IdentityProviderConfig"

type alias struct {
	typ      string
	noParent bool
}

type registration struct {
	typ, name, parent, provider  string
	aliases                      []alias
	protect, deleteBeforeReplace bool
	inputs                       resource.PropertyMap
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
		reg.deleteBeforeReplace = rpc.GetDeleteBeforeReplace()

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

func base() *eksoidc.Args {
	return &eksoidc.Args{
		ClusterName:    pulumi.String("c1"),
		ConfigName:     "issuer",
		IssuerURL:      "https://issuer.example.test",
		ClientID:       "k8s:c1",
		LegacyTopLevel: true,
	}
}

func legacy(eksoidc.Child) string { return "legacy-oidc" }

func run(t *testing.T, args *eksoidc.Args, mutate ...func(a *eksoidc.Args)) ([]registration, error) {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "aws-main", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		args.Provider = p

		for _, m := range mutate {
			m(args)
		}

		_, err = eksoidc.New(ctx, "k", args)

		return err
	}, pulumi.WithMocks("example", "dev", rec))

	return rec.regs, err
}

func children(regs []registration) []registration {
	var out []registration

	for _, r := range regs {
		if r.typ != eksoidc.TypeToken && !strings.HasPrefix(r.typ, "pulumi:providers:") {
			out = append(out, r)
		}
	}

	return out
}

func only(t *testing.T, regs []registration) registration {
	t.Helper()

	kids := children(regs)
	if len(kids) != 1 || kids[0].typ != configType {
		t.Fatalf("children = %v, want exactly one %s", kids, configType)
	}

	return kids[0]
}

func oidcInput(r registration, key string) string {
	v, ok := r.inputs["oidc"]
	if !ok || !v.IsObject() {
		return ""
	}

	if f, ok := v.ObjectValue()[resource.PropertyKey(key)]; ok && f.IsString() {
		return f.StringValue()
	}

	return ""
}

func TestDefaultName(t *testing.T) {
	regs, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	if got := only(t, regs).name; got != "k-oidc" {
		t.Errorf("name = %q, want k-oidc", got)
	}
}

func TestTheChildKeepsTheNameTheHookGives(t *testing.T) {
	a := base()
	a.Names = legacy

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if got := only(t, regs).name; got != "legacy-oidc" {
		t.Errorf("name = %q, want legacy-oidc", got)
	}
}

func TestTheChildIsUnderTheComponentAndItsProvider(t *testing.T) {
	regs, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	var comp, prov string

	for _, r := range regs {
		if r.typ == eksoidc.TypeToken {
			comp = r.name
		}

		if strings.HasPrefix(r.typ, "pulumi:providers:") {
			prov = r.name
		}
	}

	r := only(t, regs)
	if comp == "" || !strings.HasSuffix(r.parent, "::"+comp) || !strings.Contains(r.parent, eksoidc.TypeToken) {
		t.Errorf("parent %q is not the component %q", r.parent, comp)
	}

	if !strings.Contains(r.provider, prov) {
		t.Errorf("provider %q is not the caller's", r.provider)
	}
}

func TestAliasIsNoParentWithLegacyTopLevel(t *testing.T) {
	regs, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	var plain int

	for _, al := range only(t, regs).aliases {
		if al.noParent && al.typ == "" {
			plain++
		}
	}

	if plain != 1 {
		t.Errorf("want exactly one plain NoParent alias, got %d", plain)
	}
}

// The AWS SDK may declare aliases of its own on this type without a parent.
// Under a component those resolve beneath the component, so a state still
// holding the former type at the top level would be missed. ADR 0003 item 6:
// whatever the SDK aliases, the legacy set must repeat. The SDK declares none
// on this type today; this goes red when an upgrade adds one.
func TestTheSDKAliasesNoneOfThisType(t *testing.T) {
	regs, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	for _, al := range only(t, regs).aliases {
		if !al.noParent {
			t.Errorf("the SDK now aliases from %q; make LegacyTopLevel repeat it with no parent (ADR 0003 item 6)", al.typ)
		}

		if al.noParent && al.typ != "" {
			t.Errorf("a typed NoParent alias from %q the SDK does not declare", al.typ)
		}
	}
}

func TestWithoutLegacyTopLevelNoAlias(t *testing.T) {
	a := base()
	a.LegacyTopLevel = false

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if got := only(t, regs).aliases; len(got) != 0 {
		t.Errorf("aliases = %v without LegacyTopLevel", got)
	}
}

func TestProtectsByDefault(t *testing.T) {
	for name, tc := range map[string]struct {
		protect *bool
		want    bool
	}{"nil": {nil, true}, "true": {ptr(true), true}, "false": {ptr(false), false}} {
		t.Run(name, func(t *testing.T) {
			a := base()
			a.Protect = tc.protect

			regs, err := run(t, a)
			if err != nil {
				t.Fatal(err)
			}

			if got := only(t, regs).protect; got != tc.want {
				t.Errorf("protect = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeleteBeforeReplaceAlways(t *testing.T) {
	a := base()
	a.Protect = ptr(false)

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if !only(t, regs).deleteBeforeReplace {
		t.Error("deleteBeforeReplace is not set: EKS admits one association per cluster")
	}
}

func TestInputsAndDefaults(t *testing.T) {
	regs, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	r := only(t, regs)

	for key, want := range map[string]string{
		"clientId":                   "k8s:c1",
		"identityProviderConfigName": "issuer",
		"issuerUrl":                  "https://issuer.example.test",
		"usernameClaim":              "sub",
		"usernamePrefix":             "-",
		"groupsClaim":                "groups",
	} {
		if got := oidcInput(r, key); got != want {
			t.Errorf("oidc.%s = %q, want %q", key, got, want)
		}
	}

	if v, ok := r.inputs["clusterName"]; !ok || v.StringValue() != "c1" {
		t.Errorf("clusterName = %v", v)
	}
}

func TestExplicitClaimsWinOverDefaults(t *testing.T) {
	a := base()
	a.UsernameClaim, a.UsernamePrefix, a.GroupsClaim = "email", "id:", "roles"

	regs, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	r := only(t, regs)

	for key, want := range map[string]string{"usernameClaim": "email", "usernamePrefix": "id:", "groupsClaim": "roles"} {
		if got := oidcInput(r, key); got != want {
			t.Errorf("oidc.%s = %q, want %q", key, got, want)
		}
	}
}

func TestRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(a *eksoidc.Args)
		want   string
	}{
		"cluster":     {func(a *eksoidc.Args) { a.ClusterName = nil }, "ClusterName is nil"},
		"config name": {func(a *eksoidc.Args) { a.ConfigName = "" }, "ConfigName is empty"},
		"client":      {func(a *eksoidc.Args) { a.ClientID = "" }, "ClientID is empty"},
		"issuer":      {func(a *eksoidc.Args) { a.IssuerURL = "" }, "IssuerURL is empty"},
		"http issuer": {func(a *eksoidc.Args) { a.IssuerURL = "http://issuer.example.test" }, "not an https URL"},
		"bare scheme": {func(a *eksoidc.Args) { a.IssuerURL = "https://" }, "not an https URL"},
		"empty name": {func(a *eksoidc.Args) {
			a.Names = func(eksoidc.Child) string { return "" }
		}, "empty name for provider-config"},
	} {
		t.Run(name, func(t *testing.T) {
			regs, err := run(t, base(), tc.mutate)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}

			if len(children(regs)) != 0 {
				t.Errorf("registered %v before refusing", children(regs))
			}
		})
	}
}

func TestMissingProviderIsRefused(t *testing.T) {
	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := eksoidc.New(ctx, "k", base())
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
	a.ClusterName, a.ConfigName, a.ClientID, a.IssuerURL = nil, "", "", ""

	_, err := run(t, a)
	if err == nil {
		t.Fatal("want an error")
	}

	for _, want := range []string{"ClusterName is nil", "ConfigName is empty", "ClientID is empty", "IssuerURL is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestOutputsExist(t *testing.T) {
	rec := &recorder{}

	var comp *eksoidc.EksOidc

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "aws-main", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a := base()
		a.Provider = p
		comp, err = eksoidc.New(ctx, "k", a)

		return err
	}, pulumi.WithMocks("example", "dev", rec))
	if err != nil {
		t.Fatal(err)
	}

	if comp.ProviderConfig == nil {
		t.Error("ProviderConfig is not set")
	}
}
