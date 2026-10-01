package vpc_test

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

	"github.com/truvity/k8s/pkg/aws/vpc"
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

	state := args.Inputs.Copy()
	if args.TypeToken == "aws:ec2/vpc:Vpc" {
		// What AWS fills in: an IPv6 /56 from the documentation range.
		state["ipv6CidrBlock"] = resource.NewStringProperty("2001:db8:abcd:ab00::/56")
		state["defaultNetworkAclId"] = resource.NewStringProperty("acl-default")
	}

	return args.Name + "-id", state, nil
}

func (*recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func ptr[T any](v T) *T { return &v }

// base builds args for a three-zone VPC with one public and two private
// subnets; a test changes what it is about. The provider is filled in by run.
func base() *vpc.Args {
	cidrs := func(third int) map[string]string {
		return map[string]string{
			"us-east-1a": fmt.Sprintf("10.0.%d.0/24", third),
			"us-east-1b": fmt.Sprintf("10.0.%d.0/24", third+1),
			"us-east-1c": fmt.Sprintf("10.0.%d.0/24", third+2),
		}
	}

	return &vpc.Args{
		Region:            "us-east-1",
		CIDR:              "10.0.0.0/16",
		AvailabilityZones: []string{"us-east-1c", "us-east-1a", "us-east-1b"},
		Subnets: []vpc.Subnet{
			{Name: "public", Type: vpc.Public, CIDRs: cidrs(0), IPv6Index: 0},
			{Name: "svc", Type: vpc.Private, CIDRs: cidrs(4), IPv6Index: 4, Tags: map[string]string{"role/internal-lb": "1"}},
			{Name: "worker", Type: vpc.Private, CIDRs: cidrs(8), IPv6Index: 8},
		},
		LegacyTopLevel: true,
	}
}

// legacy names the children the way a stack that predates the component
// might have.
func legacy(c vpc.Child) string {
	switch c.Kind {
	case vpc.KindVPC:
		return "vpc"
	case vpc.KindDefaultSecurityGroup:
		return "default-sg"
	case vpc.KindDefaultNACL:
		return "default-nacl"
	case vpc.KindInternetGateway:
		return "igw"
	case vpc.KindEgressOnlyGateway:
		return "eoigw"
	case vpc.KindPublicRouteTable:
		return "route-table/public"
	case vpc.KindPublicRouteIPv4:
		return "route-default"
	case vpc.KindPublicRouteIPv6:
		return "route-default-ipv6"
	case vpc.KindPrivateRouteTable:
		return "route-table/private/" + c.AZ
	case vpc.KindPrivateRouteIPv6:
		return "route-ipv6-eoigw/" + c.AZ
	case vpc.KindSubnet, vpc.KindSubnetAssociation:
		return "subnet/" + c.Subnet + "/" + c.AZ
	case vpc.KindNATEIP:
		return "nat-eip"
	case vpc.KindNATGateway:
		return "nat-gateway"
	case vpc.KindNATRoute:
		return "route-nat-gw/" + c.AZ
	case vpc.KindGatewayEndpoint:
		return c.Service + "-endpoint"
	case vpc.KindFlowLogGroup:
		return "vpc-flow-logs-log-group"
	case vpc.KindFlowLogRole:
		return "vpc-flow-logs-role"
	case vpc.KindFlowLogPolicy:
		return "vpc-flow-logs-policy"
	default:
		return "vpc-flow-log"
	}
}

type result struct {
	regs []registration
	comp *vpc.Vpc
}

func run(t *testing.T, args *vpc.Args, mutate ...func(a *vpc.Args)) (result, error) {
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

		res.comp, err = vpc.New(ctx, "net", args)

		return err
	}, pulumi.WithMocks("example", "dev", rec))

	res.regs = rec.regs

	return res, err
}

func children(regs []registration) []registration {
	var out []registration

	for _, r := range regs {
		if r.typ != vpc.TypeToken && !strings.HasPrefix(r.typ, "pulumi:providers:") {
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

func ofType(regs []registration, typ string) []registration {
	var out []registration

	for _, r := range children(regs) {
		if r.typ == typ {
			out = append(out, r)
		}
	}

	return out
}

func tagsOf(r registration) map[string]string {
	out := map[string]string{}

	if v, ok := r.inputs["tags"]; ok && v.IsObject() {
		for k, x := range v.ObjectValue() {
			out[string(k)] = x.StringValue()
		}
	}

	return out
}

func str(r registration, key string) string {
	if v, ok := r.inputs[resource.PropertyKey(key)]; ok && v.IsString() {
		return v.StringValue()
	}

	return ""
}

func TestChildrenKeepTheNamesTheHookGives(t *testing.T) {
	a := base()
	a.Names = legacy

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:cloudwatch/logGroup:LogGroup vpc-flow-logs-log-group",
		"aws:ec2/defaultNetworkAcl:DefaultNetworkAcl default-nacl",
		"aws:ec2/defaultSecurityGroup:DefaultSecurityGroup default-sg",
		"aws:ec2/egressOnlyInternetGateway:EgressOnlyInternetGateway eoigw",
		"aws:ec2/eip:Eip nat-eip",
		"aws:ec2/flowLog:FlowLog vpc-flow-log",
		"aws:ec2/internetGateway:InternetGateway igw",
		"aws:ec2/natGateway:NatGateway nat-gateway",
		"aws:ec2/route:Route route-default",
		"aws:ec2/route:Route route-default-ipv6",
		"aws:ec2/routeTable:RouteTable route-table/public",
		"aws:ec2/vpc:Vpc vpc",
		"aws:ec2/vpcEndpoint:VpcEndpoint dynamodb-endpoint",
		"aws:ec2/vpcEndpoint:VpcEndpoint s3-endpoint",
		"aws:iam/role:Role vpc-flow-logs-role",
		"aws:iam/rolePolicy:RolePolicy vpc-flow-logs-policy",
	}

	for _, az := range []string{"us-east-1a", "us-east-1b", "us-east-1c"} {
		want = append(want,
			"aws:ec2/route:Route route-ipv6-eoigw/"+az,
			"aws:ec2/route:Route route-nat-gw/"+az,
			"aws:ec2/routeTable:RouteTable route-table/private/"+az)

		for _, s := range []string{"public", "svc", "worker"} {
			want = append(want,
				"aws:ec2/subnet:Subnet subnet/"+s+"/"+az,
				"aws:ec2/routeTableAssociation:RouteTableAssociation subnet/"+s+"/"+az)
		}
	}

	sort.Strings(want)

	if got := names(res.regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultNames(t *testing.T) {
	a := base()
	a.AvailabilityZones = []string{"us-east-1a"}
	a.Subnets = []vpc.Subnet{
		{Name: "public", Type: vpc.Public, CIDRs: map[string]string{"us-east-1a": "10.0.0.0/24"}},
		{Name: "app", Type: vpc.Private, CIDRs: map[string]string{"us-east-1a": "10.0.1.0/24"}, IPv6Index: 1},
	}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:cloudwatch/logGroup:LogGroup net-flow-log-group",
		"aws:ec2/defaultNetworkAcl:DefaultNetworkAcl net-default-nacl",
		"aws:ec2/defaultSecurityGroup:DefaultSecurityGroup net-default-sg",
		"aws:ec2/egressOnlyInternetGateway:EgressOnlyInternetGateway net-eoigw",
		"aws:ec2/eip:Eip net-nat-eip-us-east-1a",
		"aws:ec2/flowLog:FlowLog net-flow-log",
		"aws:ec2/internetGateway:InternetGateway net-igw",
		"aws:ec2/natGateway:NatGateway net-nat-us-east-1a",
		"aws:ec2/route:Route net-nat-route-us-east-1a",
		"aws:ec2/route:Route net-private-route-ipv6-us-east-1a",
		"aws:ec2/route:Route net-public-route-ipv4",
		"aws:ec2/route:Route net-public-route-ipv6",
		"aws:ec2/routeTable:RouteTable net-private-rt-us-east-1a",
		"aws:ec2/routeTable:RouteTable net-public-rt",
		"aws:ec2/routeTableAssociation:RouteTableAssociation net-subnet-app-us-east-1a-assoc",
		"aws:ec2/routeTableAssociation:RouteTableAssociation net-subnet-public-us-east-1a-assoc",
		"aws:ec2/subnet:Subnet net-subnet-app-us-east-1a",
		"aws:ec2/subnet:Subnet net-subnet-public-us-east-1a",
		"aws:ec2/vpc:Vpc net-vpc",
		"aws:ec2/vpcEndpoint:VpcEndpoint net-dynamodb-endpoint",
		"aws:ec2/vpcEndpoint:VpcEndpoint net-s3-endpoint",
		"aws:iam/role:Role net-flow-log-role",
		"aws:iam/rolePolicy:RolePolicy net-flow-log-policy",
	}

	if got := names(res.regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

// plan and build are two lists of the same children; they must not drift,
// or the naming hook would be checked against names that are not registered.
func TestThePlanIsWhatIsRegistered(t *testing.T) {
	for name, mut := range map[string]func(a *vpc.Args){
		"default":       func(*vpc.Args) {},
		"no IPv6":       func(a *vpc.Args) { a.DisableIPv6 = true },
		"per-zone NAT":  func(a *vpc.Args) { a.NAT = vpc.NATPerZone },
		"no NAT":        func(a *vpc.Args) { a.NAT = vpc.NATNone },
		"no endpoints":  func(a *vpc.Args) { a.GatewayEndpoints = []string{} },
		"s3 only":       func(a *vpc.Args) { a.GatewayEndpoints = []string{vpc.ServiceS3} },
		"no flow logs":  func(a *vpc.Args) { a.FlowLogs.Disable = true },
		"keep defaults": func(a *vpc.Args) { a.KeepDefaultNACL, a.KeepDefaultSecurityGroup = true, true },
		"private only, v4": func(a *vpc.Args) {
			a.DisableIPv6, a.NAT = true, vpc.NATNone
			a.Subnets = a.Subnets[1:]
		},
		"public only": func(a *vpc.Args) { a.Subnets = a.Subnets[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			// A hook that records what it is asked for is the plan.
			asked := map[string]bool{}
			a := base()
			a.Names = func(c vpc.Child) string {
				n := vpc.DefaultName(c)
				asked[n] = true

				return n
			}

			res, err := run(t, a, mut)
			if err != nil {
				t.Fatal(err)
			}

			got := map[string]bool{}
			for _, r := range children(res.regs) {
				got[r.name] = true
			}

			for n := range asked {
				if !got[n] {
					t.Errorf("planned %q but did not register it", n)
				}
			}

			for n := range got {
				if !asked[n] {
					t.Errorf("registered %q but did not plan it", n)
				}
			}
		})
	}
}

func TestEveryChildIsUnderTheComponent(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	want := "::" + vpc.TypeToken + "::net"

	for _, r := range children(res.regs) {
		if !strings.Contains(r.parent, want) {
			t.Errorf("%s %s: parent %q is not the component", r.typ, r.name, r.parent)
		}

		if !strings.Contains(r.provider, "::aws-main::") {
			t.Errorf("%s %s: provider %q is not the one given", r.typ, r.name, r.provider)
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

func TestProtectsTheVPCAndTheSubnetsByDefaultAndOnlyThem(t *testing.T) {
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
				isVPCOrSubnet := r.typ == "aws:ec2/vpc:Vpc" || r.typ == "aws:ec2/subnet:Subnet"
				if want := isVPCOrSubnet && tc.want; r.protect != want {
					t.Errorf("%s %s: protect = %v, want %v", r.typ, r.name, r.protect, want)
				}

				if r.protect {
					protected++
				}
			}

			if tc.want && protected != 10 { // the VPC and 3 subnets x 3 zones
				t.Errorf("%d protected children, want 10", protected)
			}
		})
	}
}

func TestVPCInputs(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	v := find(t, res.regs, "aws:ec2/vpc:Vpc", "net-vpc")
	if got := str(v, "cidrBlock"); got != "10.0.0.0/16" {
		t.Errorf("cidrBlock %q", got)
	}

	for _, k := range []string{"enableDnsHostnames", "enableDnsSupport", "assignGeneratedIpv6CidrBlock"} {
		if !v.inputs[resource.PropertyKey(k)].BoolValue() {
			t.Errorf("%s is not true", k)
		}
	}

	res, err = run(t, base(), func(a *vpc.Args) { a.DisableIPv6 = true })
	if err != nil {
		t.Fatal(err)
	}

	v = find(t, res.regs, "aws:ec2/vpc:Vpc", "net-vpc")
	if _, ok := v.inputs["assignGeneratedIpv6CidrBlock"]; ok {
		t.Error("IPv6 requested with DisableIPv6")
	}
}

func TestSubnets(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	// The /64 index runs from IPv6Index across the subnet's sorted zones.
	for _, tc := range []struct{ subnet, az, cidr, v6, typ string }{
		{"public", "us-east-1a", "10.0.0.0/24", "2001:db8:abcd:ab00::/64", "public"},
		{"public", "us-east-1c", "10.0.2.0/24", "2001:db8:abcd:ab02::/64", "public"},
		{"svc", "us-east-1b", "10.0.5.0/24", "2001:db8:abcd:ab05::/64", "private"},
		{"worker", "us-east-1c", "10.0.10.0/24", "2001:db8:abcd:ab0a::/64", "private"},
	} {
		s := find(t, res.regs, "aws:ec2/subnet:Subnet", fmt.Sprintf("net-subnet-%s-%s", tc.subnet, tc.az))

		if got := str(s, "cidrBlock"); got != tc.cidr {
			t.Errorf("%s %s: cidr %q, want %q", tc.subnet, tc.az, got, tc.cidr)
		}

		if got := str(s, "ipv6CidrBlock"); got != tc.v6 {
			t.Errorf("%s %s: ipv6 %q, want %q", tc.subnet, tc.az, got, tc.v6)
		}

		if got := str(s, "availabilityZone"); got != tc.az {
			t.Errorf("%s %s: zone %q", tc.subnet, tc.az, got)
		}

		if got := s.inputs["mapPublicIpOnLaunch"].BoolValue(); got != (tc.typ == "public") {
			t.Errorf("%s %s: mapPublicIpOnLaunch = %v", tc.subnet, tc.az, got)
		}

		if !s.inputs["assignIpv6AddressOnCreation"].BoolValue() {
			t.Errorf("%s %s: no IPv6 address on creation", tc.subnet, tc.az)
		}

		tags := tagsOf(s)
		for k, v := range map[string]string{"LogicalSubnet": tc.subnet, "Type": tc.typ, "AvailabilityZone": tc.az} {
			if tags[k] != v {
				t.Errorf("%s %s: tag %s = %q, want %q", tc.subnet, tc.az, k, tags[k], v)
			}
		}
	}

	if tags := tagsOf(find(t, res.regs, "aws:ec2/subnet:Subnet", "net-subnet-svc-us-east-1a")); tags["role/internal-lb"] != "1" {
		t.Errorf("Subnet.Tags not applied: %v", tags)
	}

	if tags := tagsOf(find(t, res.regs, "aws:ec2/subnet:Subnet", "net-subnet-worker-us-east-1a")); tags["role/internal-lb"] != "" {
		t.Errorf("Subnet.Tags leaked to another subnet: %v", tags)
	}
}

func TestNoIPv6AnywhereWhenDisabled(t *testing.T) {
	a := base()
	a.DisableIPv6 = true

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if strings.Contains(r.name, "ipv6") || strings.Contains(r.name, "eoigw") {
			t.Errorf("%s registered without IPv6", r.name)
		}

		for k := range r.inputs {
			if strings.Contains(strings.ToLower(string(k)), "ipv6") {
				t.Errorf("%s has IPv6 input %q", r.name, k)
			}
		}
	}

	nacl := find(t, res.regs, "aws:ec2/defaultNetworkAcl:DefaultNetworkAcl", "net-default-nacl")
	for _, dir := range []string{"ingress", "egress"} {
		for _, rule := range nacl.inputs[resource.PropertyKey(dir)].ArrayValue() {
			if _, ok := rule.ObjectValue()["ipv6CidrBlock"]; ok {
				t.Errorf("%s rule %v has an IPv6 CIDR", dir, rule.ObjectValue()["ruleNo"])
			}
		}
	}
}

func TestRouteTablesAndAssociations(t *testing.T) {
	a := base()
	a.Names = legacy

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if got := len(ofType(res.regs, "aws:ec2/routeTable:RouteTable")); got != 4 {
		t.Errorf("%d route tables, want public + 3 private", got)
	}

	for _, r := range ofType(res.regs, "aws:ec2/routeTable:RouteTable") {
		tags := tagsOf(r)
		switch r.name {
		case "route-table/public":
			if tags["Type"] != "public" {
				t.Errorf("public route table tags %v", tags)
			}
		default:
			az := strings.TrimPrefix(r.name, "route-table/private/")
			if tags["Type"] != "private" || tags["AvailabilityZone"] != az {
				t.Errorf("%s tags %v", r.name, tags)
			}
		}
	}

	// A private subnet is associated with its own zone's table, a public one
	// with the public table. The mock gives every resource the ID <name>-id.
	for _, r := range ofType(res.regs, "aws:ec2/routeTableAssociation:RouteTableAssociation") {
		parts := strings.Split(r.name, "/")
		want := "route-table/public-id"

		if parts[1] != "public" {
			want = "route-table/private/" + parts[2] + "-id"
		}

		if got := str(r, "routeTableId"); got != want {
			t.Errorf("%s: route table %q, want %q", r.name, got, want)
		}
	}

	pub := find(t, res.regs, "aws:ec2/route:Route", "route-default")
	if str(pub, "destinationCidrBlock") != "0.0.0.0/0" || str(pub, "gatewayId") != "igw-id" {
		t.Errorf("public IPv4 route: %v", pub.inputs)
	}

	pub6 := find(t, res.regs, "aws:ec2/route:Route", "route-default-ipv6")
	if str(pub6, "destinationIpv6CidrBlock") != "::/0" || str(pub6, "gatewayId") != "igw-id" {
		t.Errorf("public IPv6 route: %v", pub6.inputs)
	}

	e := find(t, res.regs, "aws:ec2/route:Route", "route-ipv6-eoigw/us-east-1b")
	if str(e, "destinationIpv6CidrBlock") != "::/0" || str(e, "egressOnlyGatewayId") != "eoigw-id" ||
		str(e, "routeTableId") != "route-table/private/us-east-1b-id" {
		t.Errorf("egress-only route: %v", e.inputs)
	}
}

func TestNATModes(t *testing.T) {
	azs := []string{"us-east-1a", "us-east-1b", "us-east-1c"}

	t.Run("single", func(t *testing.T) {
		a := base()
		a.Names = legacy

		res, err := run(t, a)
		if err != nil {
			t.Fatal(err)
		}

		if got := len(ofType(res.regs, "aws:ec2/natGateway:NatGateway")); got != 1 {
			t.Fatalf("%d NAT gateways, want 1", got)
		}

		gw := find(t, res.regs, "aws:ec2/natGateway:NatGateway", "nat-gateway")
		if got := str(gw, "subnetId"); got != "subnet/public/us-east-1a-id" {
			t.Errorf("NAT gateway in %q, want the first zone's public subnet", got)
		}

		for _, az := range azs {
			r := find(t, res.regs, "aws:ec2/route:Route", "route-nat-gw/"+az)
			if str(r, "natGatewayId") != "nat-gateway-id" || str(r, "destinationCidrBlock") != "0.0.0.0/0" ||
				str(r, "routeTableId") != "route-table/private/"+az+"-id" {
				t.Errorf("NAT route %s: %v", az, r.inputs)
			}
		}
	})

	t.Run("per zone", func(t *testing.T) {
		a := base()
		a.NAT = vpc.NATPerZone

		res, err := run(t, a)
		if err != nil {
			t.Fatal(err)
		}

		if got := len(ofType(res.regs, "aws:ec2/natGateway:NatGateway")); got != 3 {
			t.Fatalf("%d NAT gateways, want 3", got)
		}

		for _, az := range azs {
			gw := find(t, res.regs, "aws:ec2/natGateway:NatGateway", "net-nat-"+az)
			if got := str(gw, "subnetId"); got != "net-subnet-public-"+az+"-id" {
				t.Errorf("NAT gateway of %s in %q", az, got)
			}

			r := find(t, res.regs, "aws:ec2/route:Route", "net-nat-route-"+az)
			if got := str(r, "natGatewayId"); got != "net-nat-"+az+"-id" {
				t.Errorf("route of %s goes to %q, want its own zone's gateway", az, got)
			}
		}
	})

	t.Run("NATSubnet", func(t *testing.T) {
		a := base()
		a.Subnets = append([]vpc.Subnet{{Name: "edge", Type: vpc.Public, IPv6Index: 20, CIDRs: map[string]string{
			"us-east-1a": "10.0.20.0/24", "us-east-1b": "10.0.21.0/24", "us-east-1c": "10.0.22.0/24",
		}}}, a.Subnets...)
		a.NATSubnet = "public"

		res, err := run(t, a)
		if err != nil {
			t.Fatal(err)
		}

		gw := ofType(res.regs, "aws:ec2/natGateway:NatGateway")[0]
		if got := str(gw, "subnetId"); got != "net-subnet-public-us-east-1a-id" {
			t.Errorf("NAT gateway in %q, want the named subnet", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		a := base()
		a.NAT = vpc.NATNone

		res, err := run(t, a)
		if err != nil {
			t.Fatal(err)
		}

		for _, r := range children(res.regs) {
			if strings.Contains(r.typ, "nat") || strings.Contains(r.typ, "Eip") || strings.Contains(r.name, "nat") {
				t.Errorf("%s %s registered with NAT none", r.typ, r.name)
			}
		}
	})
}

func TestGatewayEndpoints(t *testing.T) {
	res, err := run(t, base(), func(a *vpc.Args) { a.Names = legacy })
	if err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{"s3", "dynamodb"} {
		ep := find(t, res.regs, "aws:ec2/vpcEndpoint:VpcEndpoint", svc+"-endpoint")
		if got := str(ep, "serviceName"); got != "com.amazonaws.us-east-1."+svc {
			t.Errorf("%s service name %q", svc, got)
		}

		if got := str(ep, "vpcEndpointType"); got != "Gateway" {
			t.Errorf("%s type %q", svc, got)
		}

		var tables []string
		for _, v := range ep.inputs["routeTableIds"].ArrayValue() {
			tables = append(tables, v.StringValue())
		}

		want := []string{
			"route-table/public-id", "route-table/private/us-east-1a-id",
			"route-table/private/us-east-1b-id", "route-table/private/us-east-1c-id",
		}
		if !slices.Equal(tables, want) {
			t.Errorf("%s route tables %q, want %q", svc, tables, want)
		}
	}

	res, err = run(t, base(), func(a *vpc.Args) { a.GatewayEndpoints = []string{vpc.ServiceS3}; a.Region = "" })
	if err == nil || !strings.Contains(err.Error(), "Region is empty") {
		t.Errorf("an endpoint with no region: %v", err)
	}

	res, err = run(t, base(), func(a *vpc.Args) { a.GatewayEndpoints = []string{}; a.Region = "" })
	if err != nil {
		t.Fatal(err)
	}

	if got := len(ofType(res.regs, "aws:ec2/vpcEndpoint:VpcEndpoint")); got != 0 {
		t.Errorf("%d endpoints with an empty list", got)
	}
}

func TestDefaultSecurityGroupAndNACL(t *testing.T) {
	a := base()
	a.DefaultNACLHTTPSIngress = []string{"10.0.0.0/14", "192.168.0.0/16"}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	sg := find(t, res.regs, "aws:ec2/defaultSecurityGroup:DefaultSecurityGroup", "net-default-sg")
	if len(sg.inputs["ingress"].ArrayValue()) != 0 || len(sg.inputs["egress"].ArrayValue()) != 0 {
		t.Errorf("default security group keeps rules: %v", sg.inputs)
	}

	nacl := find(t, res.regs, "aws:ec2/defaultNetworkAcl:DefaultNetworkAcl", "net-default-nacl")

	https := map[int]string{}
	var all []string

	for _, dir := range []string{"ingress", "egress"} {
		for _, rule := range nacl.inputs[resource.PropertyKey(dir)].ArrayValue() {
			o := rule.ObjectValue()
			n := int(o["ruleNo"].NumberValue())
			all = append(all, fmt.Sprintf("%s-%d", dir, n))

			from, to := int(o["fromPort"].NumberValue()), int(o["toPort"].NumberValue())
			if from <= 3389 && 3389 <= to && o["protocol"].StringValue() != "-1" {
				t.Errorf("%s rule %d admits RDP", dir, n)
			}

			if dir == "ingress" && from == 443 && to == 443 {
				https[n] = o["cidrBlock"].StringValue()
			}
		}
	}

	if want := map[int]string{114: "10.0.0.0/14", 115: "192.168.0.0/16"}; fmt.Sprint(https) != fmt.Sprint(want) {
		t.Errorf("HTTPS ingress rules %v, want %v", https, want)
	}

	for _, want := range []string{"ingress-100", "ingress-101", "ingress-110", "egress-100", "egress-110", "egress-153"} {
		if !slices.Contains(all, want) {
			t.Errorf("rule %s missing from %v", want, all)
		}
	}

	// The intra-VPC rule is the VPC's own CIDR.
	for _, rule := range nacl.inputs["ingress"].ArrayValue() {
		if o := rule.ObjectValue(); o["ruleNo"].NumberValue() == 100 && o["cidrBlock"].StringValue() != "10.0.0.0/16" {
			t.Errorf("rule 100 admits %q", o["cidrBlock"].StringValue())
		}
	}
}

func TestKeepDefaults(t *testing.T) {
	a := base()
	a.KeepDefaultNACL, a.KeepDefaultSecurityGroup = true, true

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if strings.Contains(r.typ, "default") {
			t.Errorf("%s registered despite the Keep flags", r.typ)
		}
	}
}

func TestFlowLogs(t *testing.T) {
	res, err := run(t, base(), func(a *vpc.Args) {
		a.FlowLogs.PermissionsBoundaryARN = pulumi.String("boundary-policy")
	})
	if err != nil {
		t.Fatal(err)
	}

	g := find(t, res.regs, "aws:cloudwatch/logGroup:LogGroup", "net-flow-log-group")
	if str(g, "name") != "/vpc/flow-logs/net" || g.inputs["retentionInDays"].NumberValue() != 365 {
		t.Errorf("log group defaults: %v", g.inputs)
	}

	role := find(t, res.regs, "aws:iam/role:Role", "net-flow-log-role")
	if str(role, "name") != "net-flow-logs" || str(role, "permissionsBoundary") != "boundary-policy" {
		t.Errorf("role: %v", role.inputs)
	}

	f := find(t, res.regs, "aws:ec2/flowLog:FlowLog", "net-flow-log")
	if str(f, "trafficType") != "ALL" || f.inputs["maxAggregationInterval"].NumberValue() != 60 ||
		str(f, "logDestinationType") != "cloud-watch-logs" {
		t.Errorf("flow log: %v", f.inputs)
	}

	res, err = run(t, base(), func(a *vpc.Args) {
		a.FlowLogs = vpc.FlowLogs{LogGroupName: "/logs/x", RoleName: "r", RetentionDays: 30, TrafficType: "REJECT", AggregationSeconds: 600}
	})
	if err != nil {
		t.Fatal(err)
	}

	g = find(t, res.regs, "aws:cloudwatch/logGroup:LogGroup", "net-flow-log-group")
	f = find(t, res.regs, "aws:ec2/flowLog:FlowLog", "net-flow-log")
	role = find(t, res.regs, "aws:iam/role:Role", "net-flow-log-role")

	if str(g, "name") != "/logs/x" || g.inputs["retentionInDays"].NumberValue() != 30 || str(role, "name") != "r" ||
		str(f, "trafficType") != "REJECT" || f.inputs["maxAggregationInterval"].NumberValue() != 600 {
		t.Errorf("overrides not applied: %v %v %v", g.inputs, role.inputs, f.inputs)
	}

	if _, ok := role.inputs["permissionsBoundary"]; ok {
		t.Error("a permissions boundary with none given")
	}

	res, err = run(t, base(), func(a *vpc.Args) { a.FlowLogs.Disable = true })
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if strings.Contains(r.typ, "logGroup") || strings.Contains(r.typ, "flowLog") || strings.Contains(r.typ, "iam/") {
			t.Errorf("%s registered with flow logs disabled", r.typ)
		}
	}
}

func TestTagPrecedence(t *testing.T) {
	a := base()
	a.Tags = map[string]string{"Owner": "team", "Type": "from-args"}
	a.ChildTags = func(c vpc.Child) map[string]string {
		out := map[string]string{"Env": "dev"}

		if c.Kind == vpc.KindVPC {
			out["Name"] = "dev-vpc"
		}

		if c.Kind == vpc.KindSubnet {
			out["Name"] = "dev-" + c.Subnet + "-" + c.AZ
			out["Env"] = "subnet-env"
		}

		return out
	}
	a.Subnets[1].Tags = map[string]string{"Env": "from-subnet"}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if tags := tagsOf(find(t, res.regs, "aws:ec2/vpc:Vpc", "net-vpc")); tags["Name"] != "dev-vpc" || tags["Owner"] != "team" || tags["Env"] != "dev" {
		t.Errorf("vpc tags %v", tags)
	}

	if tags := tagsOf(find(t, res.regs, "aws:ec2/internetGateway:InternetGateway", "net-igw")); tags["Name"] != "net-igw" {
		t.Errorf("igw tags %v: the default Name is DefaultName", tags)
	}

	// Args.Tags beat the structural tags; the hook beats Args.Tags; the
	// subnet's own tags beat the hook.
	tags := tagsOf(find(t, res.regs, "aws:ec2/subnet:Subnet", "net-subnet-svc-us-east-1a"))
	if tags["Type"] != "from-args" || tags["Env"] != "from-subnet" || tags["Name"] != "dev-svc-us-east-1a" {
		t.Errorf("subnet tags %v", tags)
	}

	// Routes and associations take no tags.
	for _, typ := range []string{"aws:ec2/route:Route", "aws:ec2/routeTableAssociation:RouteTableAssociation", "aws:iam/rolePolicy:RolePolicy"} {
		for _, r := range ofType(res.regs, typ) {
			if _, ok := r.inputs["tags"]; ok {
				t.Errorf("%s %s has tags", typ, r.name)
			}
		}
	}
}

func TestOutputs(t *testing.T) {
	res, err := run(t, base())
	if err != nil {
		t.Fatal(err)
	}

	c := res.comp
	if c.CIDR != "10.0.0.0/16" || len(c.PrivateRouteTableIDs) != 3 || len(c.NATGatewayIDs) != 1 || len(c.GatewayEndpointIDs) != 2 {
		t.Errorf("outputs: %+v", c)
	}

	if len(c.SubnetIDs) != 3 || len(c.SubnetIDs["svc"]) != 3 {
		t.Errorf("subnet IDs: %v", c.SubnetIDs)
	}

	err = pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "p", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a := base()
		a.Provider = p
		a.DisableIPv6 = true
		a.Subnets = a.Subnets[:1]

		comp, err := vpc.New(ctx, "net", a)
		if err != nil {
			return err
		}

		// Outputs for what was not asked for are empty strings, never nil.
		for _, o := range []pulumi.StringOutput{comp.VPCID, comp.IPv6CIDR, comp.EgressOnlyGatewayID, comp.FlowLogID} {
			ctx.Export("x", o)
		}

		comp.VPCID.ApplyT(func(id string) string {
			if id == "" {
				t.Error("empty VPC ID")
			}

			return id
		})

		return nil
	}, pulumi.WithMocks("example", "dev", &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(a *vpc.Args)
		want string
	}{
		"CIDR empty":          {func(a *vpc.Args) { a.CIDR = "" }, "CIDR is empty"},
		"CIDR garbage":        {func(a *vpc.Args) { a.CIDR = "ten" }, "not an IPv4 CIDR"},
		"CIDR IPv6":           {func(a *vpc.Args) { a.CIDR = "fd00::/48" }, "not an IPv4 CIDR"},
		"CIDR too big":        {func(a *vpc.Args) { a.CIDR = "10.0.0.0/8" }, "between /16 and /28"},
		"CIDR too small":      {func(a *vpc.Args) { a.CIDR = "10.0.0.0/29" }, "between /16 and /28"},
		"no provider":         {func(a *vpc.Args) { a.Provider = nil }, "Provider is nil"},
		"no zones":            {func(a *vpc.Args) { a.AvailabilityZones = nil }, "AvailabilityZones is empty"},
		"empty zone":          {func(a *vpc.Args) { a.AvailabilityZones[0] = "" }, "AvailabilityZones[0] is empty"},
		"repeated zone":       {func(a *vpc.Args) { a.AvailabilityZones[0] = "us-east-1a" }, "duplicate zone"},
		"no subnets":          {func(a *vpc.Args) { a.Subnets = nil }, "Subnets is empty"},
		"subnet no name":      {func(a *vpc.Args) { a.Subnets[0].Name = "" }, "Name is empty"},
		"subnet repeated":     {func(a *vpc.Args) { a.Subnets[1].Name = "public" }, "duplicate Name"},
		"subnet bad type":     {func(a *vpc.Args) { a.Subnets[0].Type = "dmz" }, "neither"},
		"zone count short":    {func(a *vpc.Args) { delete(a.Subnets[1].CIDRs, "us-east-1c") }, "no CIDR for zone"},
		"zone count extra":    {func(a *vpc.Args) { a.Subnets[1].CIDRs["us-east-1d"] = "10.0.30.0/24" }, "not in AvailabilityZones"},
		"zone count mismatch": {func(a *vpc.Args) { a.AvailabilityZones = a.AvailabilityZones[:2] }, "not in AvailabilityZones"},
		"subnet CIDR bad":     {func(a *vpc.Args) { a.Subnets[0].CIDRs["us-east-1a"] = "nope" }, "not an IPv4 CIDR"},
		"subnet outside":      {func(a *vpc.Args) { a.Subnets[0].CIDRs["us-east-1a"] = "192.168.0.0/24" }, "outside the VPC CIDR"},
		"subnet wider":        {func(a *vpc.Args) { a.Subnets[0].CIDRs["us-east-1a"] = "10.0.0.0/15" }, "outside the VPC CIDR"},
		"subnets identical":   {func(a *vpc.Args) { a.Subnets[1].CIDRs["us-east-1a"] = "10.0.0.0/24" }, "overlaps"},
		"subnets nested":      {func(a *vpc.Args) { a.Subnets[1].CIDRs["us-east-1a"] = "10.0.0.128/25" }, "overlaps"},
		"IPv6 index overlap":  {func(a *vpc.Args) { a.Subnets[1].IPv6Index = 1 }, "IPv6 indexes"},
		"IPv6 index range":    {func(a *vpc.Args) { a.Subnets[2].IPv6Index = 254 }, "do not fit"},
		"IPv6 index negative": {func(a *vpc.Args) { a.Subnets[2].IPv6Index = -1 }, "do not fit"},
		"NAT bad mode":        {func(a *vpc.Args) { a.NAT = "double" }, "NAT"},
		"NAT no public":       {func(a *vpc.Args) { a.Subnets = a.Subnets[1:] }, "no public subnet"},
		"NATSubnet unknown":   {func(a *vpc.Args) { a.NATSubnet = "edge" }, "not one of Subnets"},
		"NATSubnet private":   {func(a *vpc.Args) { a.NATSubnet = "svc" }, "not a public subnet"},
		"NATSubnet with none": {func(a *vpc.Args) { a.NAT = vpc.NATNone; a.NATSubnet = "public" }, "NAT is none"},
		"unknown endpoint":    {func(a *vpc.Args) { a.GatewayEndpoints = []string{"sqs"} }, "gateway endpoints"},
		"repeated endpoint":   {func(a *vpc.Args) { a.GatewayEndpoints = []string{"s3", "s3"} }, "repeats"},
		"too many HTTPS": {func(a *vpc.Args) {
			a.DefaultNACLHTTPSIngress = []string{"10.1.0.0/16", "10.2.0.0/16", "10.3.0.0/16", "10.4.0.0/16", "10.5.0.0/16", "10.6.0.0/16", "10.7.0.0/16"}
		}, "at most 6"},
		"HTTPS CIDR bad":     {func(a *vpc.Args) { a.DefaultNACLHTTPSIngress = []string{"x"} }, "not an IPv4 CIDR"},
		"retention negative": {func(a *vpc.Args) { a.FlowLogs.RetentionDays = -1 }, "negative"},
		"traffic type":       {func(a *vpc.Args) { a.FlowLogs.TrafficType = "SOME" }, "TrafficType"},
		"aggregation":        {func(a *vpc.Args) { a.FlowLogs.AggregationSeconds = 30 }, "AggregationSeconds"},
		"empty name":         {func(a *vpc.Args) { a.Names = func(vpc.Child) string { return "" } }, "empty name"},
		"repeated name":      {func(a *vpc.Args) { a.Names = func(vpc.Child) string { return "same" } }, "for both"},
	} {
		t.Run(name, func(t *testing.T) {
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
				if r.typ == vpc.TypeToken {
					t.Error("the component registered despite the refusal")
				}
			}
		})
	}
}

func TestRefusalReportsEveryProblemAtOnce(t *testing.T) {
	_, err := run(t, base(), func(a *vpc.Args) {
		a.CIDR = "10.0.0.0/8"
		a.Provider = nil
		a.AvailabilityZones = nil
		a.Names = func(vpc.Child) string { return "same" }
	})
	if err == nil {
		t.Fatal("want an error")
	}

	for _, w := range []string{"between /16 and /28", "Provider is nil", "AvailabilityZones is empty", "for both"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not mention %q", err, w)
		}
	}
}

func TestNilArgs(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := vpc.New(ctx, "net", nil)

		return err
	}, pulumi.WithMocks("example", "dev", &recorder{}))
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestValidateOnItsOwn(t *testing.T) {
	a := base()
	a.Provider = &pulumiaws.Provider{}

	if err := a.Validate(); err != nil {
		t.Fatalf("a sound args fails Validate: %v", err)
	}

	a.CIDR = ""
	if err := a.Validate(); err == nil {
		t.Fatal("an args with no CIDR passes Validate")
	}
}
