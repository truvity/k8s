package vpc

import (
	"errors"
	"fmt"
	"math/big"
	"net"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// plan lists every child the args register, in registration order.
func (a *Args) plan(component string) []Child {
	c := func(k Kind) Child { return Child{Component: component, Kind: k} }
	at := func(k Kind, az string) Child { return Child{Component: component, Kind: k, AZ: az} }

	out := []Child{c(KindVPC)}

	if !a.KeepDefaultSecurityGroup {
		out = append(out, c(KindDefaultSecurityGroup))
	}

	if !a.KeepDefaultNACL {
		out = append(out, c(KindDefaultNACL))
	}

	if a.hasPublic() {
		out = append(out, c(KindInternetGateway), c(KindPublicRouteTable), c(KindPublicRouteIPv4))

		if a.ipv6() {
			out = append(out, c(KindPublicRouteIPv6))
		}
	}

	azs := a.sortedAZs()

	if a.hasPrivate() {
		if a.ipv6() {
			out = append(out, c(KindEgressOnlyGateway))
		}

		for _, az := range azs {
			out = append(out, at(KindPrivateRouteTable, az))

			if a.ipv6() {
				out = append(out, at(KindPrivateRouteIPv6, az))
			}
		}
	}

	for _, s := range a.Subnets {
		for _, az := range azs {
			out = append(out,
				Child{Component: component, Kind: KindSubnet, Subnet: s.Name, AZ: az},
				Child{Component: component, Kind: KindSubnetAssociation, Subnet: s.Name, AZ: az})
		}
	}

	if a.wantsNAT() {
		for _, az := range a.natAZs() {
			out = append(out, at(KindNATEIP, az), at(KindNATGateway, az))
		}

		for _, az := range azs {
			out = append(out, at(KindNATRoute, az))
		}
	}

	for _, svc := range a.endpoints() {
		out = append(out, Child{Component: component, Kind: KindGatewayEndpoint, Service: svc})
	}

	if !a.FlowLogs.Disable {
		out = append(out, c(KindFlowLogGroup), c(KindFlowLogRole), c(KindFlowLogPolicy), c(KindFlowLog))
	}

	return out
}

// checkNames refuses a naming hook that returns an empty or repeated name.
func (a *Args) checkNames(component string) []error {
	names := a.Names
	if names == nil {
		names = DefaultName
	}

	var errs []error

	// A URN is type plus name, so a subnet and its route table association
	// may share one (the estate's existing state does): the name must be
	// unique among children of the same Pulumi type.
	type key struct{ typ, name string }

	used := map[key]Child{}

	for _, c := range a.plan(component) {
		n := names(c)
		if n == "" {
			errs = append(errs, fmt.Errorf("args: Names returned an empty name for %s", describe(c)))
			continue
		}

		k := key{resourceType(c.Kind), n}
		if other, dup := used[k]; dup {
			errs = append(errs, fmt.Errorf("args: Names returned %q for both %s and %s", n, describe(other), describe(c)))
		}

		used[k] = c
	}

	return errs
}

// resourceType is the Pulumi type token of the child a Kind registers.
func resourceType(k Kind) string {
	switch k {
	case KindDefaultSecurityGroup:
		return "aws:ec2/defaultSecurityGroup:DefaultSecurityGroup"
	case KindDefaultNACL:
		return "aws:ec2/defaultNetworkAcl:DefaultNetworkAcl"
	case KindInternetGateway:
		return "aws:ec2/internetGateway:InternetGateway"
	case KindEgressOnlyGateway:
		return "aws:ec2/egressOnlyInternetGateway:EgressOnlyInternetGateway"
	case KindPublicRouteTable, KindPrivateRouteTable:
		return "aws:ec2/routeTable:RouteTable"
	case KindPublicRouteIPv4, KindPublicRouteIPv6, KindPrivateRouteIPv6, KindNATRoute:
		return "aws:ec2/route:Route"
	case KindSubnet:
		return "aws:ec2/subnet:Subnet"
	case KindSubnetAssociation:
		return "aws:ec2/routeTableAssociation:RouteTableAssociation"
	case KindNATEIP:
		return "aws:ec2/eip:Eip"
	case KindNATGateway:
		return "aws:ec2/natGateway:NatGateway"
	case KindGatewayEndpoint:
		return "aws:ec2/vpcEndpoint:VpcEndpoint"
	case KindFlowLogGroup:
		return "aws:cloudwatch/logGroup:LogGroup"
	case KindFlowLogRole:
		return "aws:iam/role:Role"
	case KindFlowLogPolicy:
		return "aws:iam/rolePolicy:RolePolicy"
	case KindFlowLog:
		return "aws:ec2/flowLog:FlowLog"
	default:
		return "aws:ec2/vpc:Vpc"
	}
}

func describe(c Child) string {
	s := string(c.Kind)

	for _, v := range []string{c.Subnet, c.AZ, c.Service} {
		if v != "" {
			s += " " + v
		}
	}

	return s
}

// New registers the component and its children. It returns an error,
// registering nothing, when args.Validate does or when Names returns an empty
// or repeated name.
//
// The provider comes from Args, not from pulumi.Providers.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*Vpc, error) {
	if args == nil {
		return nil, errors.New("vpc: args is nil")
	}

	// Problems are reported together; the names can only be checked on args
	// that are otherwise sound enough to plan.
	errs := []error{args.Validate()}
	errs = append(errs, args.checkNames(name)...)

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("vpc %s: %w", name, err)
	}

	names := args.Names
	if names == nil {
		names = DefaultName
	}

	empty := pulumi.String("").ToStringOutput()

	comp := &Vpc{
		CIDR:                 args.CIDR,
		IPv6CIDR:             empty,
		InternetGatewayID:    empty,
		EgressOnlyGatewayID:  empty,
		PublicRouteTableID:   empty,
		FlowLogID:            empty,
		PrivateRouteTableIDs: map[string]pulumi.StringOutput{},
		SubnetIDs:            map[string]map[string]pulumi.StringOutput{},
		NATGatewayIDs:        map[string]pulumi.StringOutput{},
		NATEIPIDs:            map[string]pulumi.StringOutput{},
		GatewayEndpointIDs:   map[string]pulumi.StringOutput{},
	}

	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	b := &builder{ctx: ctx, comp: comp, args: args, name: name, names: names}
	if err := b.build(); err != nil {
		return nil, err
	}

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{
		"vpcId":    comp.VPCID,
		"ipv6Cidr": comp.IPv6CIDR,
	}); err != nil {
		return nil, err
	}

	return comp, nil
}

type builder struct {
	ctx   *pulumi.Context
	comp  *Vpc
	args  *Args
	name  string
	names NameFunc

	vpc       *ec2.Vpc
	igw       *ec2.InternetGateway
	eoigw     *ec2.EgressOnlyInternetGateway
	publicRT  *ec2.RouteTable
	privateRT map[string]*ec2.RouteTable
	subnets   map[string]map[string]*ec2.Subnet
	nat       map[string]*ec2.NatGateway
	allRTIDs  []pulumi.StringInput
	ipv6CIDR  pulumi.StringOutput
	azs       []string
}

func (b *builder) child(c Child) string {
	c.Component = b.name

	return b.names(c)
}

// childOpts is what every child registers with: the component as parent, the
// provider, and the adoption alias when asked for.
func (b *builder) childOpts(extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := append([]pulumi.ResourceOption{pulumi.Parent(b.comp), pulumi.Provider(b.args.Provider)}, extra...)

	if b.args.LegacyTopLevel {
		out = append(out, pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}}))
	}

	return out
}

func (b *builder) protectOpt() []pulumi.ResourceOption {
	if b.args.protect() {
		return []pulumi.ResourceOption{pulumi.Protect(true)}
	}

	return nil
}

// tags builds a child's tags: Name, the structural tags, Args.Tags, then the
// hook's.
func (b *builder) tags(c Child, structural map[string]string, last map[string]string) pulumi.StringMap {
	c.Component = b.name

	m := pulumi.StringMap{"Name": pulumi.String(DefaultName(c))}

	for _, src := range []map[string]string{structural, b.args.Tags} {
		for k, v := range src {
			m[k] = pulumi.String(v)
		}
	}

	if b.args.ChildTags != nil {
		for k, v := range b.args.ChildTags(c) {
			m[k] = pulumi.String(v)
		}
	}

	for k, v := range last {
		m[k] = pulumi.String(v)
	}

	return m
}

func (b *builder) build() error {
	steps := []func() error{
		b.createVPC,
		b.defaults,
		b.publicNetwork,
		b.privateNetwork,
		b.createSubnets,
		b.createNAT,
		b.createEndpoints,
		b.createFlowLogs,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}

	return nil
}

func (b *builder) createVPC() error {
	a := b.args
	b.azs = a.sortedAZs()

	vpcArgs := &ec2.VpcArgs{
		CidrBlock:          pulumi.String(a.CIDR),
		EnableDnsHostnames: pulumi.Bool(true),
		EnableDnsSupport:   pulumi.Bool(true),
		Tags:               b.tags(Child{Kind: KindVPC}, nil, nil),
	}
	if a.ipv6() {
		vpcArgs.AssignGeneratedIpv6CidrBlock = pulumi.Bool(true)
	}

	v, err := ec2.NewVpc(b.ctx, b.child(Child{Kind: KindVPC}), vpcArgs, b.childOpts(b.protectOpt()...)...)
	if err != nil {
		return fmt.Errorf("create VPC: %w", err)
	}

	b.vpc = v
	b.comp.VPCID = v.ID().ToStringOutput()

	if a.ipv6() {
		b.ipv6CIDR = v.Ipv6CidrBlock
		b.comp.IPv6CIDR = v.Ipv6CidrBlock
	}

	return nil
}

func (b *builder) publicNetwork() error {
	a := b.args
	if !a.hasPublic() {
		return nil
	}

	igw, err := ec2.NewInternetGateway(b.ctx, b.child(Child{Kind: KindInternetGateway}), &ec2.InternetGatewayArgs{
		VpcId: b.vpc.ID(),
		Tags:  b.tags(Child{Kind: KindInternetGateway}, nil, nil),
	}, b.childOpts()...)
	if err != nil {
		return fmt.Errorf("create internet gateway: %w", err)
	}

	b.igw = igw
	b.comp.InternetGatewayID = igw.ID().ToStringOutput()

	rt, err := ec2.NewRouteTable(b.ctx, b.child(Child{Kind: KindPublicRouteTable}), &ec2.RouteTableArgs{
		VpcId: b.vpc.ID(),
		Tags:  b.tags(Child{Kind: KindPublicRouteTable}, map[string]string{"Type": string(Public)}, nil),
	}, b.childOpts()...)
	if err != nil {
		return fmt.Errorf("create public route table: %w", err)
	}

	b.publicRT = rt
	b.comp.PublicRouteTableID = rt.ID().ToStringOutput()
	b.allRTIDs = append(b.allRTIDs, rt.ID().ToStringOutput())

	if _, err := ec2.NewRoute(b.ctx, b.child(Child{Kind: KindPublicRouteIPv4}), &ec2.RouteArgs{
		RouteTableId:         rt.ID(),
		DestinationCidrBlock: pulumi.String("0.0.0.0/0"),
		GatewayId:            igw.ID(),
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("create default route: %w", err)
	}

	if a.ipv6() {
		if _, err := ec2.NewRoute(b.ctx, b.child(Child{Kind: KindPublicRouteIPv6}), &ec2.RouteArgs{
			RouteTableId:             rt.ID(),
			DestinationIpv6CidrBlock: pulumi.String("::/0"),
			GatewayId:                igw.ID(),
		}, b.childOpts()...); err != nil {
			return fmt.Errorf("create IPv6 default route: %w", err)
		}
	}

	return nil
}

func (b *builder) privateNetwork() error {
	a := b.args
	if !a.hasPrivate() {
		return nil
	}

	if a.ipv6() {
		e, err := ec2.NewEgressOnlyInternetGateway(b.ctx, b.child(Child{Kind: KindEgressOnlyGateway}), &ec2.EgressOnlyInternetGatewayArgs{
			VpcId: b.vpc.ID(),
			Tags:  b.tags(Child{Kind: KindEgressOnlyGateway}, nil, nil),
		}, b.childOpts()...)
		if err != nil {
			return fmt.Errorf("create egress-only internet gateway: %w", err)
		}

		b.eoigw = e
		b.comp.EgressOnlyGatewayID = e.ID().ToStringOutput()
	}

	b.privateRT = map[string]*ec2.RouteTable{}

	for _, az := range b.azs {
		c := Child{Kind: KindPrivateRouteTable, AZ: az}

		rt, err := ec2.NewRouteTable(b.ctx, b.child(c), &ec2.RouteTableArgs{
			VpcId: b.vpc.ID(),
			Tags:  b.tags(c, map[string]string{"Type": string(Private), "AvailabilityZone": az}, nil),
		}, b.childOpts()...)
		if err != nil {
			return fmt.Errorf("create private route table for %s: %w", az, err)
		}

		b.privateRT[az] = rt
		b.comp.PrivateRouteTableIDs[az] = rt.ID().ToStringOutput()

		if a.ipv6() {
			if _, err := ec2.NewRoute(b.ctx, b.child(Child{Kind: KindPrivateRouteIPv6, AZ: az}), &ec2.RouteArgs{
				RouteTableId:             rt.ID(),
				DestinationIpv6CidrBlock: pulumi.String("::/0"),
				EgressOnlyGatewayId:      b.eoigw.ID(),
			}, b.childOpts()...); err != nil {
				return fmt.Errorf("add egress-only route for %s: %w", az, err)
			}
		}
	}

	// The endpoints attach to the public route table first, then the private
	// ones in zone order.
	for _, az := range b.azs {
		b.allRTIDs = append(b.allRTIDs, b.privateRT[az].ID().ToStringOutput())
	}

	return nil
}

func (b *builder) createSubnets() error {
	a := b.args
	b.subnets = map[string]map[string]*ec2.Subnet{}

	for _, s := range a.Subnets {
		b.subnets[s.Name] = map[string]*ec2.Subnet{}
		b.comp.SubnetIDs[s.Name] = map[string]pulumi.StringOutput{}

		for i, az := range b.azs {
			c := Child{Kind: KindSubnet, Subnet: s.Name, AZ: az}

			sa := &ec2.SubnetArgs{
				VpcId:               b.vpc.ID(),
				CidrBlock:           pulumi.String(s.CIDRs[az]),
				AvailabilityZone:    pulumi.String(az),
				MapPublicIpOnLaunch: pulumi.Bool(s.Type == Public),
				Tags: b.tags(c, map[string]string{
					"LogicalSubnet":    s.Name,
					"Type":             string(s.Type),
					"AvailabilityZone": az,
				}, s.Tags),
			}

			if a.ipv6() {
				index := s.IPv6Index + i

				sa.Ipv6CidrBlock = b.ipv6CIDR.ApplyT(func(vpcCIDR string) (string, error) {
					return ipv6Subnet64(vpcCIDR, index)
				}).(pulumi.StringOutput)
				sa.AssignIpv6AddressOnCreation = pulumi.Bool(true)
			}

			sn, err := ec2.NewSubnet(b.ctx, b.child(c), sa, b.childOpts(b.protectOpt()...)...)
			if err != nil {
				return fmt.Errorf("create subnet %s in %s: %w", s.Name, az, err)
			}

			b.subnets[s.Name][az] = sn
			b.comp.SubnetIDs[s.Name][az] = sn.ID().ToStringOutput()

			rt := b.publicRT
			if s.Type == Private {
				rt = b.privateRT[az]
			}

			if _, err := ec2.NewRouteTableAssociation(b.ctx, b.child(Child{Kind: KindSubnetAssociation, Subnet: s.Name, AZ: az}),
				&ec2.RouteTableAssociationArgs{SubnetId: sn.ID(), RouteTableId: rt.ID()}, b.childOpts()...); err != nil {
				return fmt.Errorf("associate subnet %s in %s with its route table: %w", s.Name, az, err)
			}
		}
	}

	return nil
}

func (b *builder) createNAT() error {
	a := b.args
	if !a.wantsNAT() {
		return nil
	}

	b.nat = map[string]*ec2.NatGateway{}
	host := a.natSubnet()

	for _, az := range a.natAZs() {
		eipChild := Child{Kind: KindNATEIP, AZ: az}

		eip, err := ec2.NewEip(b.ctx, b.child(eipChild), &ec2.EipArgs{
			Domain: pulumi.String("vpc"),
			Tags:   b.tags(eipChild, nil, nil),
		}, b.childOpts()...)
		if err != nil {
			return fmt.Errorf("create NAT gateway address in %s: %w", az, err)
		}

		natChild := Child{Kind: KindNATGateway, AZ: az}

		gw, err := ec2.NewNatGateway(b.ctx, b.child(natChild), &ec2.NatGatewayArgs{
			SubnetId:     b.subnets[host][az].ID(),
			AllocationId: eip.AllocationId,
			Tags:         b.tags(natChild, nil, nil),
		}, b.childOpts()...)
		if err != nil {
			return fmt.Errorf("create NAT gateway in %s: %w", az, err)
		}

		b.nat[az] = gw
		b.comp.NATEIPIDs[az] = eip.ID().ToStringOutput()
		b.comp.NATGatewayIDs[az] = gw.ID().ToStringOutput()
	}

	for _, az := range b.azs {
		gw := b.nat[az]
		if gw == nil {
			gw = b.nat[a.natAZs()[0]]
		}

		if _, err := ec2.NewRoute(b.ctx, b.child(Child{Kind: KindNATRoute, AZ: az}), &ec2.RouteArgs{
			RouteTableId:         b.privateRT[az].ID(),
			DestinationCidrBlock: pulumi.String("0.0.0.0/0"),
			NatGatewayId:         gw.ID(),
		}, b.childOpts()...); err != nil {
			return fmt.Errorf("add NAT route for %s: %w", az, err)
		}
	}

	return nil
}

func (b *builder) createEndpoints() error {
	for _, svc := range b.args.endpoints() {
		c := Child{Kind: KindGatewayEndpoint, Service: svc}

		ep, err := ec2.NewVpcEndpoint(b.ctx, b.child(c), &ec2.VpcEndpointArgs{
			VpcId:           b.vpc.ID(),
			ServiceName:     pulumi.Sprintf("com.amazonaws.%s.%s", b.args.Region, svc),
			VpcEndpointType: pulumi.String("Gateway"),
			RouteTableIds:   pulumi.StringArray(b.allRTIDs),
			Tags:            b.tags(c, nil, nil),
		}, b.childOpts()...)
		if err != nil {
			return fmt.Errorf("create %s gateway endpoint: %w", svc, err)
		}

		b.comp.GatewayEndpointIDs[svc] = ep.ID().ToStringOutput()
	}

	return nil
}

// ipv6Subnet64 carves a /64 out of a VPC's /56 (or any prefix up to /64) by
// index, like cidrsubnet(vpcCIDR, 8, index) for a /56.
func ipv6Subnet64(vpcIPv6CIDR string, index int) (string, error) {
	ip, network, err := net.ParseCIDR(vpcIPv6CIDR)
	if err != nil {
		return "", fmt.Errorf("parse VPC IPv6 CIDR %q: %w", vpcIPv6CIDR, err)
	}

	ones, bits := network.Mask.Size()

	const newOnes = 64
	if ones > newOnes {
		return "", fmt.Errorf("VPC IPv6 prefix /%d is already longer than /64", ones)
	}

	baseIP := ip.Mask(network.Mask).To16()
	n := new(big.Int).SetBytes(baseIP)
	n.Add(n, new(big.Int).Lsh(big.NewInt(int64(index)), uint(bits-newOnes)))

	raw := n.Bytes()
	if len(raw) < 16 {
		padded := make([]byte, 16)
		copy(padded[16-len(raw):], raw)
		raw = padded
	}

	return fmt.Sprintf("%s/%d", net.IP(raw[:16]).String(), newOnes), nil
}
