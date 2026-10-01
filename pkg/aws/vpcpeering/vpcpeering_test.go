package vpcpeering_test

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/vpcpeering"
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

// base builds args with every input a test may need; a test removes or adds
// what it is about. The providers are filled in by run.
func base() *vpcpeering.Args {
	return &vpcpeering.Args{
		RequesterVPCID:    pulumi.String("vpc-req"),
		AccepterVPCID:     pulumi.String("vpc-acc"),
		AccepterAccountID: "acct-accepter",
		RequesterCIDR:     "10.64.0.0/16",
		AccepterCIDR:      "10.65.0.0/16",
		RequesterIPv6CIDR: pulumi.String("2600:1f18::/56"),
		AccepterIPv6CIDR:  pulumi.String("2600:1f19::/56"),
		RequesterRouteTables: map[string]pulumi.StringInput{
			"eu-central-1a": pulumi.String("rtb-r1"),
			"public":        pulumi.String("rtb-r2"),
		},
		AccepterRouteTables: map[string]pulumi.StringInput{
			"eu-central-1a": pulumi.String("rtb-a1"),
			"public":        pulumi.String("rtb-a2"),
		},
		Tags:           map[string]string{"ManagedBy": "pulumi"},
		LegacyTopLevel: true,
	}
}

// legacy reproduces the names the estate's peering stack used before it
// became a component.
func legacy(c vpcpeering.Child) string {
	switch c.Kind {
	case vpcpeering.KindConnection:
		return "peering/pcx/requester/hub-spoke"
	case vpcpeering.KindAccepter:
		return "peering/pcx/accepter/hub-spoke"
	case vpcpeering.KindRoute:
		from, to := "hub", "spoke"
		if c.Side == vpcpeering.Accepter {
			from, to = to, from
		}

		return fmt.Sprintf("peering/%s/%s/%s/%s", c.Family, from, to, c.Key)
	case vpcpeering.KindZoneAuthorization:
		return "zone-assoc/" + c.Key + "/auth"
	case vpcpeering.KindZoneAssociation:
		return "zone-assoc/" + c.Key + "/assoc"
	default:
		return "peering/dns/" + string(c.Side)
	}
}

func crossZone() vpcpeering.ZoneAssociation {
	return vpcpeering.ZoneAssociation{
		Name: "spoke-private-to-hub", ZoneID: pulumi.String("Z1"), VPCID: pulumi.String("vpc-req"),
		Region: "eu-central-1", CrossAccount: true,
	}
}

type result struct {
	regs []registration
}

func run(t *testing.T, args *vpcpeering.Args, mutate ...func(a *vpcpeering.Args, req, acc *pulumiaws.Provider)) (result, error) {
	t.Helper()

	rec := &recorder{}

	var res result

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		req, err := pulumiaws.NewProvider(ctx, "requester", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		acc, err := pulumiaws.NewProvider(ctx, "accepter", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		args.RequesterProvider = req
		args.AccepterProvider = acc

		for i := range args.ZoneAssociations {
			if args.ZoneAssociations[i].CrossAccount {
				args.ZoneAssociations[i].ZoneProvider = acc
				args.ZoneAssociations[i].VPCProvider = req
			}
		}

		for _, m := range mutate {
			m(args, req, acc)
		}

		_, err = vpcpeering.New(ctx, "peering", args)

		return err
	}, pulumi.WithMocks("example", "dev", rec))

	res.regs = rec.regs

	return res, err
}

func children(regs []registration) []registration {
	var out []registration

	for _, r := range regs {
		if r.typ != vpcpeering.TypeToken && !strings.HasPrefix(r.typ, "pulumi:providers:") {
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

func TestChildrenKeepTheNamesTheHookGives(t *testing.T) {
	a := base()
	a.Names = legacy
	a.ZoneAssociations = []vpcpeering.ZoneAssociation{crossZone()}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:ec2/vpcPeeringConnection:VpcPeeringConnection peering/pcx/requester/hub-spoke",
		"aws:ec2/vpcPeeringConnectionAccepter:VpcPeeringConnectionAccepter peering/pcx/accepter/hub-spoke",
		"aws:route53/vpcAssociationAuthorization:VpcAssociationAuthorization zone-assoc/spoke-private-to-hub/auth",
		"aws:route53/zoneAssociation:ZoneAssociation zone-assoc/spoke-private-to-hub/assoc",
	}

	for _, fam := range []string{"ipv4", "ipv6"} {
		for _, k := range []string{"eu-central-1a", "public"} {
			want = append(want,
				fmt.Sprintf("aws:ec2/route:Route peering/%s/hub/spoke/%s", fam, k),
				fmt.Sprintf("aws:ec2/route:Route peering/%s/spoke/hub/%s", fam, k))
		}
	}

	sort.Strings(want)

	if got := names(res.regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultNames(t *testing.T) {
	a := base()
	a.RequesterIPv6CIDR, a.AccepterIPv6CIDR = nil, nil
	a.RequesterDNSResolution = ptr(true)

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:ec2/peeringConnectionOptions:PeeringConnectionOptions peering-dns-requester",
		"aws:ec2/route:Route peering-route-accepter-ipv4-eu-central-1a",
		"aws:ec2/route:Route peering-route-accepter-ipv4-public",
		"aws:ec2/route:Route peering-route-requester-ipv4-eu-central-1a",
		"aws:ec2/route:Route peering-route-requester-ipv4-public",
		"aws:ec2/vpcPeeringConnection:VpcPeeringConnection peering-connection",
		"aws:ec2/vpcPeeringConnectionAccepter:VpcPeeringConnectionAccepter peering-accepter",
	}

	if got := names(res.regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestNoIPv6RoutesWithoutBothIPv6CIDRs(t *testing.T) {
	a := base()
	a.RequesterIPv6CIDR, a.AccepterIPv6CIDR = nil, nil

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if strings.Contains(r.name, "ipv6") {
			t.Errorf("%s registered without IPv6 CIDRs", r.name)
		}
	}
}

func TestEveryChildIsUnderTheComponent(t *testing.T) {
	a := base()
	a.ZoneAssociations = []vpcpeering.ZoneAssociation{crossZone()}
	a.RequesterDNSResolution, a.AccepterDNSResolution = ptr(true), ptr(false)

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := "::" + vpcpeering.TypeToken + "::peering"

	for _, r := range children(res.regs) {
		if !strings.Contains(r.parent, want) {
			t.Errorf("%s %s: parent %q is not the component", r.typ, r.name, r.parent)
		}
	}
}

func TestEachChildUsesTheProviderOfItsSide(t *testing.T) {
	a := base()
	a.Names = legacy
	a.ZoneAssociations = []vpcpeering.ZoneAssociation{crossZone()}
	a.RequesterDNSResolution, a.AccepterDNSResolution = ptr(true), ptr(true)

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	// Zone: authorized by the zone's account (accepter), associated by the
	// VPC's (requester), as crossZone sets them.
	want := map[string]string{
		"peering/pcx/requester/hub-spoke":       "requester",
		"peering/pcx/accepter/hub-spoke":        "accepter",
		"peering/ipv4/hub/spoke/public":         "requester",
		"peering/ipv4/spoke/hub/public":         "accepter",
		"peering/ipv6/hub/spoke/public":         "requester",
		"peering/ipv6/spoke/hub/public":         "accepter",
		"peering/dns/requester":                 "requester",
		"peering/dns/accepter":                  "accepter",
		"zone-assoc/spoke-private-to-hub/auth":  "accepter",
		"zone-assoc/spoke-private-to-hub/assoc": "requester",
	}

	for _, r := range children(res.regs) {
		w, ok := want[r.name]
		if !ok {
			continue
		}

		if got := providerName(res.regs, r.provider); got != w {
			t.Errorf("%s: provider %q, want the %s one", r.name, r.provider, w)
		}
	}
}

// providerName resolves a "urn::id" provider reference to the logical name of
// the provider registration it points at.
func providerName(regs []registration, ref string) string {
	for _, r := range regs {
		if strings.HasPrefix(r.typ, "pulumi:providers:") && strings.Contains(ref, "::"+r.name+"::") {
			return r.name
		}
	}

	return ref
}

func TestAliasesAreNoParentOnEveryChildWithLegacyTopLevel(t *testing.T) {
	a := base()
	a.ZoneAssociations = []vpcpeering.ZoneAssociation{crossZone()}
	a.RequesterDNSResolution = ptr(true)

	res, err := run(t, a)
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
	a := base()
	a.ZoneAssociations = []vpcpeering.ZoneAssociation{crossZone()}
	a.RequesterDNSResolution = ptr(true)

	res, err := run(t, a)
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

func TestProtectsTheConnectionByDefaultAndOnlyIt(t *testing.T) {
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

			for _, r := range children(res.regs) {
				isConn := strings.HasPrefix(r.typ, "aws:ec2/vpcPeeringConnection")
				if want := isConn && tc.want; r.protect != want {
					t.Errorf("%s %s: protect = %v, want %v", r.typ, r.name, r.protect, want)
				}
			}
		})
	}
}

func TestRoutesPointAtTheOtherSide(t *testing.T) {
	a := base()
	a.Names = legacy

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]struct{ key, val string }{
		"peering/ipv4/hub/spoke/public": {"destinationCidrBlock", "10.65.0.0/16"},
		"peering/ipv4/spoke/hub/public": {"destinationCidrBlock", "10.64.0.0/16"},
		"peering/ipv6/hub/spoke/public": {"destinationIpv6CidrBlock", "2600:1f19::/56"},
		"peering/ipv6/spoke/hub/public": {"destinationIpv6CidrBlock", "2600:1f18::/56"},
	} {
		r := find(t, res.regs, "aws:ec2/route:Route", name)
		if got := r.inputs[resource.PropertyKey(want.key)].StringValue(); got != want.val {
			t.Errorf("%s %s = %q, want %q", name, want.key, got, want.val)
		}
	}

	if got := find(t, res.regs, "aws:ec2/route:Route", "peering/ipv4/hub/spoke/public").inputs["routeTableId"].StringValue(); got != "rtb-r2" {
		t.Errorf("requester route table = %q, want rtb-r2", got)
	}

	if got := find(t, res.regs, "aws:ec2/route:Route", "peering/ipv4/spoke/hub/public").inputs["routeTableId"].StringValue(); got != "rtb-a2" {
		t.Errorf("accepter route table = %q, want rtb-a2", got)
	}
}

func TestConnectionInputs(t *testing.T) {
	a := base()
	a.AccepterRegion = "eu-west-1"

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	c := find(t, res.regs, "aws:ec2/vpcPeeringConnection:VpcPeeringConnection", "peering-connection")

	for k, want := range map[string]string{"peerOwnerId": "acct-accepter", "peerRegion": "eu-west-1", "peerVpcId": "vpc-acc", "vpcId": "vpc-req"} {
		if got := c.inputs[resource.PropertyKey(k)].StringValue(); got != want {
			t.Errorf("connection %s = %q, want %q", k, got, want)
		}
	}

	if v := c.inputs["autoAccept"]; v.IsBool() && v.BoolValue() {
		t.Error("the requester auto-accepts; the accepter resource accepts")
	}

	acc := find(t, res.regs, "aws:ec2/vpcPeeringConnectionAccepter:VpcPeeringConnectionAccepter", "peering-accepter")
	if !acc.inputs["autoAccept"].BoolValue() {
		t.Error("the accepter does not accept")
	}

	for _, r := range []registration{c, acc} {
		if r.inputs["tags"].ObjectValue()["ManagedBy"].StringValue() != "pulumi" {
			t.Errorf("%s: tags not set", r.name)
		}
	}
}

func TestDNSOptionsAreOneBlockPerSide(t *testing.T) {
	a := base()
	a.RequesterDNSResolution, a.AccepterDNSResolution = ptr(true), ptr(false)

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	req := find(t, res.regs, "aws:ec2/peeringConnectionOptions:PeeringConnectionOptions", "peering-dns-requester")
	if !req.inputs["requester"].ObjectValue()["allowRemoteVpcDnsResolution"].BoolValue() || req.inputs["accepter"].HasValue() && !req.inputs["accepter"].IsNull() {
		t.Errorf("requester options = %v", req.inputs)
	}

	acc := find(t, res.regs, "aws:ec2/peeringConnectionOptions:PeeringConnectionOptions", "peering-dns-accepter")
	if v := acc.inputs["accepter"].ObjectValue()["allowRemoteVpcDnsResolution"]; !v.IsBool() || v.BoolValue() {
		t.Errorf("accepter options = %v", acc.inputs)
	}
}

func TestZoneAssociations(t *testing.T) {
	same := vpcpeering.ZoneAssociation{Name: "local", ZoneID: pulumi.String("Z2"), VPCID: pulumi.String("vpc-acc"), Region: "eu-central-1"}
	a := base()
	a.ZoneAssociations = []vpcpeering.ZoneAssociation{crossZone(), same}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	find(t, res.regs, "aws:route53/vpcAssociationAuthorization:VpcAssociationAuthorization", "peering-zone-spoke-private-to-hub-auth")
	find(t, res.regs, "aws:route53/zoneAssociation:ZoneAssociation", "peering-zone-spoke-private-to-hub-assoc")
	find(t, res.regs, "aws:route53/zoneAssociation:ZoneAssociation", "peering-zone-local-assoc")

	for _, r := range res.regs {
		if r.name == "peering-zone-local-auth" {
			t.Error("a same-account association registers an authorization")
		}
	}
}

func TestRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(a *vpcpeering.Args, req, acc *pulumiaws.Provider)
		want string
	}{
		"identical CIDRs":        {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterCIDR = a.RequesterCIDR }, "overlap"},
		"nested CIDRs":           {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterCIDR = "10.64.8.0/24" }, "overlap"},
		"CIDR not IPv4":          {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterCIDR = "fd00::/8" }, "not an IPv4 CIDR"},
		"CIDR garbage":           {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.RequesterCIDR = "ten" }, "not an IPv4 CIDR"},
		"CIDR empty":             {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.RequesterCIDR = "" }, "RequesterCIDR is empty"},
		"no requester provider":  {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.RequesterProvider = nil }, "RequesterProvider is nil"},
		"no accepter provider":   {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterProvider = nil }, "AccepterProvider is nil"},
		"empty requester tables": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.RequesterRouteTables = nil }, "RequesterRouteTables is empty"},
		"empty accepter tables": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) {
			a.AccepterRouteTables = map[string]pulumi.StringInput{}
		}, "AccepterRouteTables is empty"},
		"nil route table":       {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterRouteTables["public"] = nil }, "is nil"},
		"empty route table key": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterRouteTables[""] = pulumi.String("rtb") }, "empty key"},
		"half IPv6":             {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterIPv6CIDR = nil }, "both RequesterIPv6CIDR"},
		"no VPC":                {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterVPCID = nil }, "AccepterVPCID is nil"},
		"no account":            {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) { a.AccepterAccountID = "" }, "AccepterAccountID is empty"},
		"empty name": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) {
			a.Names = func(vpcpeering.Child) string { return "" }
		}, "empty name"},
		"repeated name": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) {
			a.Names = func(vpcpeering.Child) string { return "same" }
		}, "for both"},
		"cross-account zone without providers": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) {
			z := crossZone()
			a.ZoneAssociations = []vpcpeering.ZoneAssociation{z}
		}, "needs ZoneProvider and VPCProvider"},
		"zone without name": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) {
			a.ZoneAssociations = []vpcpeering.ZoneAssociation{{ZoneID: pulumi.String("Z"), VPCID: pulumi.String("v"), Region: "r"}}
		}, "Name is empty"},
		"duplicate zone": {func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) {
			z := vpcpeering.ZoneAssociation{Name: "z", ZoneID: pulumi.String("Z"), VPCID: pulumi.String("v"), Region: "r"}
			a.ZoneAssociations = []vpcpeering.ZoneAssociation{z, z}
		}, "duplicate Name"},
	} {
		t.Run(name, func(t *testing.T) {
			// Run the mutation after the providers are set, then check that
			// nothing was registered besides the providers.
			res, err := run(t, base(), tc.mut)
			if err == nil {
				t.Fatal("want an error, got none")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}

			if n := len(children(res.regs)); n != 0 {
				t.Errorf("%d resources registered despite the refusal", n)
			}

			for _, r := range res.regs {
				if r.typ == vpcpeering.TypeToken {
					t.Error("the component registered despite the refusal")
				}
			}
		})
	}
}

func TestRefusalReportsEveryProblemAtOnce(t *testing.T) {
	_, err := run(t, base(), func(a *vpcpeering.Args, _, _ *pulumiaws.Provider) {
		a.AccepterCIDR = a.RequesterCIDR
		a.RequesterProvider = nil
		a.AccepterRouteTables = nil
	})
	if err == nil {
		t.Fatal("want an error")
	}

	for _, w := range []string{"overlap", "RequesterProvider is nil", "AccepterRouteTables is empty"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not mention %q", err, w)
		}
	}
}

func TestConnectionIDIsAnOutput(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "p", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a := base()
		a.RequesterProvider, a.AccepterProvider = p, p

		c, err := vpcpeering.New(ctx, "peering", a)
		if err != nil {
			return err
		}

		c.ConnectionID.ApplyT(func(id string) string {
			if id == "" {
				t.Error("empty connection ID")
			}

			return id
		})

		return nil
	}, pulumi.WithMocks("example", "dev", &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
}
