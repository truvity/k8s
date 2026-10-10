package vpc

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:k8s/aws:Vpc"

// Kind names which child a logical name or a tag set is for.
type Kind string

const (
	// KindVPC is the ec2.Vpc.
	KindVPC Kind = "vpc"
	// KindDefaultSecurityGroup is the ec2.DefaultSecurityGroup that empties
	// the VPC's default group.
	KindDefaultSecurityGroup Kind = "default-sg"
	// KindDefaultNACL is the ec2.DefaultNetworkAcl that owns the default
	// ACL's rule list.
	KindDefaultNACL Kind = "default-nacl"
	// KindInternetGateway is the ec2.InternetGateway.
	KindInternetGateway Kind = "igw"
	// KindEgressOnlyGateway is the ec2.EgressOnlyInternetGateway.
	KindEgressOnlyGateway Kind = "eoigw"
	// KindPublicRouteTable is the one ec2.RouteTable of the public subnets.
	KindPublicRouteTable Kind = "public-route-table"
	// KindPublicRouteIPv4 is the public route table's 0.0.0.0/0 route.
	KindPublicRouteIPv4 Kind = "public-route-ipv4"
	// KindPublicRouteIPv6 is the public route table's ::/0 route.
	KindPublicRouteIPv6 Kind = "public-route-ipv6"
	// KindPrivateRouteTable is an ec2.RouteTable of one zone's private
	// subnets; Child.AZ says which.
	KindPrivateRouteTable Kind = "private-route-table"
	// KindPrivateRouteIPv6 is a private route table's ::/0 route to the
	// egress-only gateway; Child.AZ says which.
	KindPrivateRouteIPv6 Kind = "private-route-ipv6"
	// KindSubnet is an ec2.Subnet; Child.Subnet and Child.AZ say which.
	KindSubnet Kind = "subnet"
	// KindSubnetAssociation is the ec2.RouteTableAssociation of a subnet;
	// Child.Subnet and Child.AZ say which.
	KindSubnetAssociation Kind = "subnet-association"
	// KindNATEIP is the ec2.Eip of a NAT gateway; Child.AZ is the zone the
	// gateway sits in.
	KindNATEIP Kind = "nat-eip"
	// KindNATGateway is an ec2.NatGateway; Child.AZ is its zone.
	KindNATGateway Kind = "nat-gateway"
	// KindNATRoute is a private route table's 0.0.0.0/0 route to a NAT
	// gateway; Child.AZ is the private route table's zone.
	KindNATRoute Kind = "nat-route"
	// KindGatewayEndpoint is an ec2.VpcEndpoint of type Gateway;
	// Child.Service says which.
	KindGatewayEndpoint Kind = "gateway-endpoint"
	// KindFlowLogGroup is the cloudwatch.LogGroup of the flow logs.
	KindFlowLogGroup Kind = "flow-log-group"
	// KindFlowLogRole is the iam.Role the flow logs assume.
	KindFlowLogRole Kind = "flow-log-role"
	// KindFlowLogPolicy is the iam.RolePolicy of that role.
	KindFlowLogPolicy Kind = "flow-log-policy"
	// KindFlowLog is the ec2.FlowLog.
	KindFlowLog Kind = "flow-log"
)

// SubnetType is whether a subnet routes to the internet gateway or to NAT.
type SubnetType string

const (
	// Public subnets use the public route table (a route to the internet
	// gateway) and map a public IPv4 address on launch.
	Public SubnetType = "public"
	// Private subnets use their zone's private route table.
	Private SubnetType = "private"
)

// NATMode is how the private route tables reach the internet over IPv4.
type NATMode string

const (
	// NATSingle puts one NAT gateway in the first zone's NAT subnet and
	// routes every private route table to it. This is the default: it is
	// the cheapest layout, and a zone failure is repaired by an apply.
	NATSingle NATMode = "single"
	// NATPerZone puts a NAT gateway in every zone and routes each private
	// route table to its own zone's gateway.
	NATPerZone NATMode = "per-zone"
	// NATNone creates no NAT gateway and no IPv4 default route in the
	// private route tables.
	NATNone NATMode = "none"
)

// Gateway endpoint services.
const (
	// ServiceS3 is the S3 gateway endpoint.
	ServiceS3 = "s3"
	// ServiceDynamoDB is the DynamoDB gateway endpoint.
	ServiceDynamoDB = "dynamodb"
)

// maxHTTPSIngress is how many extra sources Args.DefaultNACLHTTPSIngress
// takes: the default network ACL's rules 114 to 119.
const maxHTTPSIngress = 6

// maxPeerEgress is how many entries Args.DefaultNACLPeerEgress takes: the
// default network ACL's outbound rules 160 to 179.
const maxPeerEgress = 20

type (
	// PeerEgress is one outbound TCP allowance of the default network ACL:
	// connections from the VPC to one port of one IPv4 CIDR.
	PeerEgress struct {
		// CIDR is the destination, an IPv4 CIDR; 0.0.0.0/0 is refused.
		CIDR string
		// Port is the destination TCP port, 1 to 65535.
		Port int
	}
)

// Child identifies one child for a NameFunc or a tag hook.
type Child struct {
	// Component is the logical name the component was registered with.
	Component string
	Kind      Kind
	// Subnet is the logical subnet name, for KindSubnet and
	// KindSubnetAssociation.
	Subnet string
	// AZ is the availability zone, for the kinds that belong to one.
	AZ string
	// Service is the endpoint service, for KindGatewayEndpoint.
	Service string
}

// NameFunc returns the Pulumi logical name of a child. The returned name must
// be unique among the component's children; it is part of the child's URN.
type NameFunc func(Child) string

// DefaultName names the children from the component name <c>: "<c>-vpc",
// "<c>-default-sg", "<c>-default-nacl", "<c>-igw", "<c>-eoigw",
// "<c>-public-rt", "<c>-public-route-ipv4", "<c>-public-route-ipv6",
// "<c>-private-rt-<az>", "<c>-private-route-ipv6-<az>",
// "<c>-subnet-<subnet>-<az>", "<c>-subnet-<subnet>-<az>-assoc",
// "<c>-nat-eip-<az>", "<c>-nat-<az>", "<c>-nat-route-<az>",
// "<c>-<service>-endpoint", "<c>-flow-log-group", "<c>-flow-log-role",
// "<c>-flow-log-policy" and "<c>-flow-log". These names are API.
func DefaultName(c Child) string {
	switch c.Kind {
	case KindDefaultSecurityGroup:
		return c.Component + "-default-sg"
	case KindDefaultNACL:
		return c.Component + "-default-nacl"
	case KindInternetGateway:
		return c.Component + "-igw"
	case KindEgressOnlyGateway:
		return c.Component + "-eoigw"
	case KindPublicRouteTable:
		return c.Component + "-public-rt"
	case KindPublicRouteIPv4:
		return c.Component + "-public-route-ipv4"
	case KindPublicRouteIPv6:
		return c.Component + "-public-route-ipv6"
	case KindPrivateRouteTable:
		return c.Component + "-private-rt-" + c.AZ
	case KindPrivateRouteIPv6:
		return c.Component + "-private-route-ipv6-" + c.AZ
	case KindSubnet:
		return fmt.Sprintf("%s-subnet-%s-%s", c.Component, c.Subnet, c.AZ)
	case KindSubnetAssociation:
		return fmt.Sprintf("%s-subnet-%s-%s-assoc", c.Component, c.Subnet, c.AZ)
	case KindNATEIP:
		return c.Component + "-nat-eip-" + c.AZ
	case KindNATGateway:
		return c.Component + "-nat-" + c.AZ
	case KindNATRoute:
		return c.Component + "-nat-route-" + c.AZ
	case KindGatewayEndpoint:
		return c.Component + "-" + c.Service + "-endpoint"
	case KindFlowLogGroup:
		return c.Component + "-flow-log-group"
	case KindFlowLogRole:
		return c.Component + "-flow-log-role"
	case KindFlowLogPolicy:
		return c.Component + "-flow-log-policy"
	case KindFlowLog:
		return c.Component + "-flow-log"
	default:
		return c.Component + "-vpc"
	}
}

// Subnet is one logical subnet: the same role in every availability zone.
type Subnet struct {
	// Name identifies the subnet among the component's children and feeds
	// the names the naming hook receives. Required, unique. It is also the
	// LogicalSubnet tag.
	Name string
	// Type is Public or Private. Required.
	Type SubnetType
	// CIDRs holds the subnet's IPv4 CIDR in each availability zone, by zone
	// name. It must have exactly one entry per Args.AvailabilityZones zone,
	// each inside Args.CIDR, none overlapping another subnet's.
	CIDRs map[string]string
	// IPv6Index is the index of the subnet's first /64 inside the VPC's
	// /56. The subnet's zones, in sorted order, take IPv6Index, IPv6Index+1
	// and so on. An explicit index keeps one subnet's /64s stable when
	// another subnet is added or removed. Used only when IPv6 is on; the
	// range must fit the /56 (0 to 255) and not overlap another subnet's.
	IPv6Index int
	// Tags are added to the subnet in every zone (for example the tags a
	// load-balancer controller discovers subnets by). They are applied
	// after the component's own tags and the ChildTags hook's, and win.
	Tags map[string]string
}

// FlowLogs configures the VPC flow logs: a CloudWatch Logs log group, the
// role the flow log service assumes to write to it, and the flow log.
type FlowLogs struct {
	// Disable turns the flow logs, their log group and their role off.
	Disable bool
	// LogGroupName is the log group's name. Default
	// "/vpc/flow-logs/<component name>".
	LogGroupName string
	// RoleName is the IAM role's name. Default
	// "<component name>-flow-logs".
	RoleName string
	// RetentionDays is the log group's retention. Default 365.
	RetentionDays int
	// TrafficType is ALL, ACCEPT or REJECT. Default ALL.
	TrafficType string
	// AggregationSeconds is the maximum aggregation interval: 60 or 600.
	// Default 60.
	AggregationSeconds int
	// PermissionsBoundaryARN, when set, is the role's permissions boundary.
	PermissionsBoundaryARN pulumi.StringInput
}

// Args configures the component.
type Args struct {
	// Provider is the AWS provider every child is registered with. Required:
	// the component never falls back to a default provider.
	Provider pulumi.ProviderResource
	// Region is the AWS region, for the gateway endpoints' service names
	// ("com.amazonaws.<region>.<service>"). Required when there are gateway
	// endpoints (the default).
	Region string

	// CIDR is the VPC's IPv4 CIDR, between /16 and /28. Required.
	CIDR string
	// DisableIPv6 turns IPv6 off. By default the VPC requests an
	// Amazon-provided /56, each subnet gets a /64 and the subnets are
	// assigned IPv6 addresses on creation; the public route table gets a
	// ::/0 route to the internet gateway and each private route table a
	// ::/0 route to an egress-only gateway.
	DisableIPv6 bool

	// AvailabilityZones are the zones the VPC spans. Required. Every
	// subnet has one CIDR in each of them, and there is one private route
	// table per zone.
	AvailabilityZones []string
	// Subnets are the logical subnets. Required. Their order decides which
	// public subnet NATSubnet defaults to.
	Subnets []Subnet

	// NAT is how private subnets reach the internet over IPv4. Default
	// NATSingle. It has an effect only when there is a private subnet.
	NAT NATMode
	// NATSubnet names the public subnet the NAT gateways sit in. Default:
	// the first public subnet in Subnets.
	NATSubnet string

	// GatewayEndpoints lists the gateway endpoints to create, from
	// ServiceS3 and ServiceDynamoDB. Nil means both; a non-nil empty slice
	// means none. Each endpoint is attached to the public route table and
	// every private route table.
	GatewayEndpoints []string

	// KeepDefaultSecurityGroup leaves the VPC's default security group
	// alone. By default the component empties it (no ingress, no egress).
	KeepDefaultSecurityGroup bool
	// KeepDefaultNACL leaves the VPC's default network ACL alone. By
	// default the component owns it with a restrictive rule set: all
	// traffic inside the VPC, ephemeral TCP and UDP return traffic, DNS and
	// NTP, HTTPS out, and no admin ports from anywhere. The component is
	// then the ACL's sole owner (see the package comment).
	KeepDefaultNACL bool
	// DefaultNACLHTTPSIngress lists IPv4 CIDRs (at most six) allowed to
	// reach port 443 inside the VPC, as rules 114 to 119 of the default
	// network ACL. Use it for peered VPCs. Ignored with KeepDefaultNACL.
	DefaultNACLHTTPSIngress []string
	// DefaultNACLPeerEgress lists outbound TCP allowances (at most twenty)
	// to one port of one IPv4 CIDR, as rules 160 to 179 of the default
	// network ACL, in the order given, after all the other rules. Use it
	// for low ports the ephemeral range does not cover (SSH, HTTP) on a
	// peered VPC. Empty adds nothing. Ignored with KeepDefaultNACL.
	DefaultNACLPeerEgress []PeerEgress

	// FlowLogs configures the flow logs. The zero value is the defaults.
	FlowLogs FlowLogs

	// Tags are set on every child that takes tags. A child gets, in
	// increasing precedence: Name (DefaultName of the child), the
	// component's own structural tags (LogicalSubnet, Type and
	// AvailabilityZone on subnets, Type and AvailabilityZone on route
	// tables), Tags, ChildTags and, on a subnet, Subnet.Tags.
	Tags map[string]string
	// ChildTags returns extra tags for one child, such as a Name tag. Nil
	// adds none. It is called for the kinds that take tags: the VPC, the
	// default security group and ACL, both gateways, the route tables, the
	// subnets, the NAT gateways and their addresses, the gateway endpoints,
	// and the flow log, its log group and its role.
	ChildTags func(Child) map[string]string

	// Names overrides the logical names of the children. Nil uses
	// DefaultName.
	Names NameFunc
	// LegacyTopLevel makes every child carry an alias from the URN it has
	// when it is registered directly under the stack (no parent), with the
	// same type and the name Names gives it. Set it when adopting resources
	// that were created before they were wrapped in this component.
	LegacyTopLevel bool
	// Protect marks the VPC and the subnets protected, so a preview that
	// would delete or replace them fails. Nil means true. Point it at false
	// only for a VPC that is meant to be torn down. No other child is
	// protected: they are rebuilt from this program.
	Protect *bool
}

func (a *Args) protect() bool { return a.Protect == nil || *a.Protect }

func (a *Args) ipv6() bool { return !a.DisableIPv6 }

func (a *Args) natMode() NATMode {
	if a.NAT == "" {
		return NATSingle
	}

	return a.NAT
}

func (a *Args) endpoints() []string {
	if a.GatewayEndpoints == nil {
		return []string{ServiceS3, ServiceDynamoDB}
	}

	return a.GatewayEndpoints
}

func (a *Args) sortedAZs() []string {
	out := slices.Clone(a.AvailabilityZones)
	sort.Strings(out)

	return out
}

func (a *Args) hasPrivate() bool {
	for _, s := range a.Subnets {
		if s.Type == Private {
			return true
		}
	}

	return false
}

func (a *Args) hasPublic() bool {
	for _, s := range a.Subnets {
		if s.Type == Public {
			return true
		}
	}

	return false
}

// natSubnet returns the name of the subnet the NAT gateways sit in, or "".
func (a *Args) natSubnet() string {
	if a.NATSubnet != "" {
		return a.NATSubnet
	}

	for _, s := range a.Subnets {
		if s.Type == Public {
			return s.Name
		}
	}

	return ""
}

// wantsNAT says whether the NAT gateways are created.
func (a *Args) wantsNAT() bool { return a.natMode() != NATNone && a.hasPrivate() }

// natAZs is the zones the NAT gateways sit in.
func (a *Args) natAZs() []string {
	azs := a.sortedAZs()
	if a.natMode() == NATSingle && len(azs) > 0 {
		return azs[:1]
	}

	return azs
}

func parseV4(field, v string) (netip.Prefix, error) {
	if v == "" {
		return netip.Prefix{}, fmt.Errorf("args: %s is empty", field)
	}

	p, err := netip.ParsePrefix(v)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("args: %s %q is not an IPv4 CIDR", field, v)
	}

	return p.Masked(), nil
}

func (a *Args) checkVPC() []error {
	var errs []error

	if a.Provider == nil {
		errs = append(errs, errors.New("args: Provider is nil"))
	}

	if p, err := parseV4("CIDR", a.CIDR); err != nil {
		errs = append(errs, err)
	} else if p.Bits() < 16 || p.Bits() > 28 {
		errs = append(errs, fmt.Errorf("args: CIDR %s must be between /16 and /28: AWS refuses any other VPC size", p))
	}

	if len(a.endpoints()) > 0 && a.Region == "" {
		errs = append(errs, errors.New("args: Region is empty: the gateway endpoints need it for their service names (or set GatewayEndpoints to an empty slice)"))
	}

	return errs
}

func (a *Args) checkAZs() []error {
	if len(a.AvailabilityZones) == 0 {
		return []error{errors.New("args: AvailabilityZones is empty")}
	}

	var errs []error

	seen := map[string]bool{}

	for i, az := range a.AvailabilityZones {
		switch {
		case az == "":
			errs = append(errs, fmt.Errorf("args: AvailabilityZones[%d] is empty", i))
		case seen[az]:
			errs = append(errs, fmt.Errorf("args: AvailabilityZones[%d]: duplicate zone %q", i, az))
		}

		seen[az] = true
	}

	return errs
}

func (a *Args) checkSubnets() []error {
	if len(a.Subnets) == 0 {
		return []error{errors.New("args: Subnets is empty")}
	}

	vpcCIDR, vpcErr := parseV4("CIDR", a.CIDR)

	var errs []error

	zones := map[string]bool{}
	for _, az := range a.AvailabilityZones {
		zones[az] = true
	}

	names := map[string]bool{}
	cidrs := map[netip.Prefix]string{}

	type span struct {
		name       string
		start, end int
	}

	var spans []span

	for i, s := range a.Subnets {
		where := fmt.Sprintf("Subnets[%d]", i)
		if s.Name != "" {
			where = fmt.Sprintf("Subnets[%d] (%q)", i, s.Name)
		}

		switch {
		case s.Name == "":
			errs = append(errs, fmt.Errorf("args: Subnets[%d]: Name is empty", i))
		case names[s.Name]:
			errs = append(errs, fmt.Errorf("args: %s: duplicate Name", where))
		}

		names[s.Name] = true

		if s.Type != Public && s.Type != Private {
			errs = append(errs, fmt.Errorf("args: %s: Type %q is neither %q nor %q", where, s.Type, Public, Private))
		}

		for az := range s.CIDRs {
			if !zones[az] {
				errs = append(errs, fmt.Errorf("args: %s: CIDRs has zone %q, which is not in AvailabilityZones", where, az))
			}
		}

		for _, az := range a.sortedAZs() {
			v, ok := s.CIDRs[az]
			if !ok {
				errs = append(errs, fmt.Errorf("args: %s: no CIDR for zone %q: every subnet spans every zone in AvailabilityZones (%d)",
					where, az, len(a.AvailabilityZones)))
				continue
			}

			p, err := parseV4(fmt.Sprintf("%s CIDRs[%q]", where, az), v)
			if err != nil {
				errs = append(errs, err)
				continue
			}

			if p.Bits() < 16 || p.Bits() > 28 {
				errs = append(errs, fmt.Errorf("args: %s CIDRs[%q] %s must be between /16 and /28", where, az, p))
			}

			if vpcErr == nil && (!vpcCIDR.Contains(p.Addr()) || p.Bits() < vpcCIDR.Bits()) {
				errs = append(errs, fmt.Errorf("args: %s CIDRs[%q] %s is outside the VPC CIDR %s", where, az, p, vpcCIDR))
			}

			for other, owner := range cidrs {
				if other.Overlaps(p) {
					errs = append(errs, fmt.Errorf("args: %s CIDRs[%q] %s overlaps %s of %s", where, az, p, other, owner))
				}
			}

			cidrs[p] = fmt.Sprintf("%s in %s", s.Name, az)
		}

		if a.ipv6() && len(s.CIDRs) > 0 {
			start, end := s.IPv6Index, s.IPv6Index+len(s.CIDRs)-1
			if start < 0 || end > 255 {
				errs = append(errs, fmt.Errorf("args: %s: IPv6 indexes %d to %d do not fit the VPC's /56 (0 to 255)", where, start, end))
			}

			for _, o := range spans {
				if start <= o.end && o.start <= end {
					errs = append(errs, fmt.Errorf("args: %s: IPv6 indexes %d to %d overlap subnet %q (%d to %d)", where, start, end, o.name, o.start, o.end))
				}
			}

			spans = append(spans, span{s.Name, start, end})
		}
	}

	return errs
}

func (a *Args) checkNAT() []error {
	var errs []error

	switch a.natMode() {
	case NATSingle, NATPerZone, NATNone:
	default:
		return []error{fmt.Errorf("args: NAT %q is not %q, %q or %q", a.NAT, NATSingle, NATPerZone, NATNone)}
	}

	if a.NATSubnet != "" && a.natMode() == NATNone {
		errs = append(errs, errors.New("args: NATSubnet is set but NAT is none"))
	}

	if !a.wantsNAT() {
		return errs
	}

	name := a.natSubnet()
	if name == "" {
		return append(errs, errors.New("args: there are private subnets but no public subnet for the NAT gateway (add one, or set NAT to none)"))
	}

	for _, s := range a.Subnets {
		if s.Name != name {
			continue
		}

		if s.Type != Public {
			errs = append(errs, fmt.Errorf("args: NATSubnet %q is not a public subnet", name))
		}

		return errs
	}

	return append(errs, fmt.Errorf("args: NATSubnet %q is not one of Subnets", name))
}

func (a *Args) checkMisc() []error {
	var errs []error

	seen := map[string]bool{}

	for _, s := range a.endpoints() {
		switch {
		case s != ServiceS3 && s != ServiceDynamoDB:
			errs = append(errs, fmt.Errorf("args: GatewayEndpoints has %q: only %q and %q are gateway endpoints", s, ServiceS3, ServiceDynamoDB))
		case seen[s]:
			errs = append(errs, fmt.Errorf("args: GatewayEndpoints repeats %q", s))
		}

		seen[s] = true
	}

	if len(a.DefaultNACLHTTPSIngress) > maxHTTPSIngress {
		errs = append(errs, fmt.Errorf("args: DefaultNACLHTTPSIngress has %d entries, at most %d fit", len(a.DefaultNACLHTTPSIngress), maxHTTPSIngress))
	}

	for i, c := range a.DefaultNACLHTTPSIngress {
		if _, err := parseV4(fmt.Sprintf("DefaultNACLHTTPSIngress[%d]", i), c); err != nil {
			errs = append(errs, err)
		}
	}

	if len(a.DefaultNACLPeerEgress) > maxPeerEgress {
		errs = append(errs, fmt.Errorf("args: DefaultNACLPeerEgress has %d entries, at most %d fit", len(a.DefaultNACLPeerEgress), maxPeerEgress))
	}

	for i, e := range a.DefaultNACLPeerEgress {
		field := fmt.Sprintf("DefaultNACLPeerEgress[%d]", i)

		if p, err := parseV4(field+".CIDR", e.CIDR); err != nil {
			errs = append(errs, err)
		} else if p.Bits() == 0 {
			errs = append(errs, fmt.Errorf("args: %s.CIDR %q is the whole internet", field, e.CIDR))
		}

		if e.Port < 1 || e.Port > 65535 {
			errs = append(errs, fmt.Errorf("args: %s.Port %d is not a port from 1 to 65535", field, e.Port))
		}
	}

	f := a.FlowLogs
	if f.RetentionDays < 0 {
		errs = append(errs, fmt.Errorf("args: FlowLogs.RetentionDays %d is negative", f.RetentionDays))
	}

	switch f.TrafficType {
	case "", "ALL", "ACCEPT", "REJECT":
	default:
		errs = append(errs, fmt.Errorf("args: FlowLogs.TrafficType %q is not ALL, ACCEPT or REJECT", f.TrafficType))
	}

	switch f.AggregationSeconds {
	case 0, 60, 600:
	default:
		errs = append(errs, fmt.Errorf("args: FlowLogs.AggregationSeconds %d is neither 60 nor 600", f.AggregationSeconds))
	}

	return errs
}

// Validate reports every problem with args at once, or returns nil. It does
// not check the naming hook, which needs the component name (New does).
func (a *Args) Validate() error {
	var errs []error

	errs = append(errs, a.checkVPC()...)
	errs = append(errs, a.checkAZs()...)
	errs = append(errs, a.checkSubnets()...)
	errs = append(errs, a.checkNAT()...)
	errs = append(errs, a.checkMisc()...)

	return errors.Join(errs...)
}

// Vpc is the component. An output for something the args did not ask for is
// an empty-string output, so it can always be exported.
type Vpc struct {
	pulumi.ResourceState

	// VPCID is the ID of the VPC.
	VPCID pulumi.StringOutput
	// CIDR is the VPC's IPv4 CIDR, as given.
	CIDR string
	// IPv6CIDR is the VPC's Amazon-provided /56; empty with DisableIPv6.
	IPv6CIDR pulumi.StringOutput
	// InternetGatewayID is empty when there is no public subnet.
	InternetGatewayID pulumi.StringOutput
	// EgressOnlyGatewayID is empty without IPv6 or private subnets.
	EgressOnlyGatewayID pulumi.StringOutput
	// PublicRouteTableID is empty when there is no public subnet.
	PublicRouteTableID pulumi.StringOutput
	// PrivateRouteTableIDs maps a zone to its private route table; empty
	// without private subnets.
	PrivateRouteTableIDs map[string]pulumi.StringOutput
	// SubnetIDs maps a subnet name to its subnets' IDs by zone.
	SubnetIDs map[string]map[string]pulumi.StringOutput
	// NATGatewayIDs and NATEIPIDs map the zone a NAT gateway sits in to its
	// ID and its address's ID. One entry with NATSingle, none with NATNone.
	NATGatewayIDs map[string]pulumi.StringOutput
	NATEIPIDs     map[string]pulumi.StringOutput
	// GatewayEndpointIDs maps a service to its endpoint's ID.
	GatewayEndpointIDs map[string]pulumi.StringOutput
	// FlowLogID is empty with FlowLogs.Disable.
	FlowLogID pulumi.StringOutput
}
