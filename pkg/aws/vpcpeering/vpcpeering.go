package vpcpeering

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/route53"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:k8s/aws:VpcPeering"

// Kind names which child a logical name is for.
type Kind string

const (
	// KindConnection is the ec2.VpcPeeringConnection, created on the
	// requester side.
	KindConnection Kind = "connection"
	// KindAccepter is the ec2.VpcPeeringConnectionAccepter, on the accepter
	// side.
	KindAccepter Kind = "accepter"
	// KindRoute is an ec2.Route; Child.Side, Child.Family and Child.Key say
	// which.
	KindRoute Kind = "route"
	// KindDNSOptions is an ec2.PeeringConnectionOptions; Child.Side says
	// which.
	KindDNSOptions Kind = "dns-options"
	// KindZoneAuthorization is the route53.VpcAssociationAuthorization of a
	// cross-account zone association; Child.Key is ZoneAssociation.Name.
	KindZoneAuthorization Kind = "zone-authorization"
	// KindZoneAssociation is a route53.ZoneAssociation; Child.Key is
	// ZoneAssociation.Name.
	KindZoneAssociation Kind = "zone-association"
)

// Side is one end of the connection.
type Side string

const (
	// Requester is the side that creates the connection.
	Requester Side = "requester"
	// Accepter is the side that accepts it.
	Accepter Side = "accepter"
)

// Family is an address family of a route.
type Family string

const (
	// IPv4 routes carry the other VPC's IPv4 CIDR.
	IPv4 Family = "ipv4"
	// IPv6 routes carry the other VPC's IPv6 CIDR.
	IPv6 Family = "ipv6"
)

// Child identifies one child for a NameFunc.
type Child struct {
	// Component is the logical name the component was registered with.
	Component string
	Kind      Kind
	// Side is set for KindRoute and KindDNSOptions: the side the route
	// table (or the option block) belongs to.
	Side Side
	// Family is set for KindRoute.
	Family Family
	// Key is the RouteTables key of a KindRoute, or the Name of a
	// ZoneAssociation for the zone kinds. Empty otherwise.
	Key string
}

// NameFunc returns the Pulumi logical name of a child. The returned name must
// be unique among the component's children; it is part of the child's URN.
type NameFunc func(Child) string

// DefaultName names the children from the component name: "<c>-connection",
// "<c>-accepter", "<c>-route-<side>-<family>-<key>", "<c>-dns-<side>",
// "<c>-zone-<key>-auth" and "<c>-zone-<key>-assoc". These names are API.
func DefaultName(c Child) string {
	switch c.Kind {
	case KindAccepter:
		return c.Component + "-accepter"
	case KindRoute:
		return fmt.Sprintf("%s-route-%s-%s-%s", c.Component, c.Side, c.Family, c.Key)
	case KindDNSOptions:
		return fmt.Sprintf("%s-dns-%s", c.Component, c.Side)
	case KindZoneAuthorization:
		return fmt.Sprintf("%s-zone-%s-auth", c.Component, c.Key)
	case KindZoneAssociation:
		return fmt.Sprintf("%s-zone-%s-assoc", c.Component, c.Key)
	default:
		return c.Component + "-connection"
	}
}

// ZoneAssociation associates a private hosted zone with a VPC. When the zone
// and the VPC are in different accounts (CrossAccount), the zone's owner
// authorizes the VPC first and the VPC's owner then creates the association.
type ZoneAssociation struct {
	// Name identifies the association among the component's children and
	// feeds the names the naming hook receives. Required, unique.
	Name string
	// ZoneID is the private hosted zone. Required.
	ZoneID pulumi.StringInput
	// VPCID is the VPC to associate. Required.
	VPCID pulumi.StringInput
	// Region is the VPC's region. Required.
	Region string
	// CrossAccount says the zone and the VPC are in different accounts.
	CrossAccount bool
	// ZoneProvider is the provider of the zone's account (it creates the
	// authorization). Required when CrossAccount; otherwise it creates the
	// association when VPCProvider is nil.
	ZoneProvider pulumi.ProviderResource
	// VPCProvider is the provider of the VPC's account (it creates the
	// association). Required when CrossAccount.
	VPCProvider pulumi.ProviderResource
}

// Args configures the component.
type Args struct {
	// RequesterVPCID and AccepterVPCID are the two VPCs. Required.
	RequesterVPCID pulumi.StringInput
	AccepterVPCID  pulumi.StringInput
	// AccepterAccountID is the account that owns the accepter VPC
	// (PeerOwnerId). Required.
	AccepterAccountID string
	// AccepterRegion is the accepter VPC's region, for a cross-region
	// peering. Empty means the requester's region.
	AccepterRegion string

	// RequesterCIDR and AccepterCIDR are the VPCs' primary IPv4 CIDRs.
	// Required; they must not overlap. A route on one side sends the OTHER
	// side's CIDR to the connection.
	RequesterCIDR string
	AccepterCIDR  string
	// RequesterIPv6CIDR and AccepterIPv6CIDR are the VPCs' IPv6 CIDRs. Set
	// both for IPv6 routes, or neither.
	RequesterIPv6CIDR pulumi.StringInput
	AccepterIPv6CIDR  pulumi.StringInput

	// RequesterRouteTables and AccepterRouteTables hold, by a caller-chosen
	// key (an AZ name, "public"), the route tables to add the route to.
	// Each side needs at least one. The key feeds Child.Key.
	RequesterRouteTables map[string]pulumi.StringInput
	AccepterRouteTables  map[string]pulumi.StringInput

	// RequesterProvider and AccepterProvider are the AWS providers of the
	// two accounts and regions. Required: the component never falls back to
	// a default provider, because a default would silently create the
	// accepter's resources in the requester's account.
	RequesterProvider pulumi.ProviderResource
	AccepterProvider  pulumi.ProviderResource

	// RequesterDNSResolution and AccepterDNSResolution set
	// AllowRemoteVpcDnsResolution for that side's VPC: whether its hosts
	// resolve the other VPC's public DNS names to private addresses. Nil
	// leaves the side's option alone and creates no options child for it.
	RequesterDNSResolution *bool
	AccepterDNSResolution  *bool

	// ZoneAssociations lists private hosted zones to associate with a VPC.
	ZoneAssociations []ZoneAssociation

	// Tags are set on both the connection and the accepter.
	Tags map[string]string

	// Names overrides the logical names of the children. Nil uses
	// DefaultName.
	Names NameFunc
	// LegacyTopLevel makes every child carry an alias from the URN it has
	// when it is registered directly under the stack (no parent), with the
	// same type and the name Names gives it. Set it when adopting resources
	// that were created before they were wrapped in this component.
	LegacyTopLevel bool
	// Protect marks the connection and the accepter protected, so a preview
	// that would delete or replace them fails. Nil means true: deleting the
	// connection cuts all traffic between the two VPCs. Point it at false
	// only for a peering that is meant to be torn down.
	Protect *bool
}

func (a *Args) protect() bool { return a.Protect == nil || *a.Protect }

// VpcPeering is the component.
type VpcPeering struct {
	pulumi.ResourceState

	// ConnectionID is the ID of the peering connection (pcx-...).
	ConnectionID pulumi.StringOutput
}

func sortedKeys(m map[string]pulumi.StringInput) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

func (a *Args) checkRouteTables(side string, m map[string]pulumi.StringInput) []error {
	if len(m) == 0 {
		return []error{fmt.Errorf("args: %sRouteTables is empty: a peering with no route is not a peering", side)}
	}

	var errs []error

	for _, k := range sortedKeys(m) {
		switch {
		case k == "":
			errs = append(errs, fmt.Errorf("args: %sRouteTables has an empty key", side))
		case m[k] == nil:
			errs = append(errs, fmt.Errorf("args: %sRouteTables[%q] is nil", side, k))
		}
	}

	return errs
}

func (a *Args) checkCIDRs() []error {
	var errs []error

	parse := func(field, v string) (netip.Prefix, bool) {
		if v == "" {
			errs = append(errs, fmt.Errorf("args: %s is empty", field))
			return netip.Prefix{}, false
		}

		p, err := netip.ParsePrefix(v)
		if err != nil || !p.Addr().Is4() {
			errs = append(errs, fmt.Errorf("args: %s %q is not an IPv4 CIDR", field, v))
			return netip.Prefix{}, false
		}

		return p.Masked(), true
	}

	req, ok1 := parse("RequesterCIDR", a.RequesterCIDR)
	acc, ok2 := parse("AccepterCIDR", a.AccepterCIDR)

	if ok1 && ok2 && req.Overlaps(acc) {
		errs = append(errs, fmt.Errorf("args: RequesterCIDR %s and AccepterCIDR %s overlap: AWS refuses to peer them", req, acc))
	}

	if (a.RequesterIPv6CIDR == nil) != (a.AccepterIPv6CIDR == nil) {
		errs = append(errs, errors.New("args: set both RequesterIPv6CIDR and AccepterIPv6CIDR, or neither"))
	}

	return errs
}

func (a *Args) checkZones() []error {
	var errs []error

	seen := map[string]bool{}

	for i, z := range a.ZoneAssociations {
		switch {
		case z.Name == "":
			errs = append(errs, fmt.Errorf("args: ZoneAssociations[%d]: Name is empty", i))
		case seen[z.Name]:
			errs = append(errs, fmt.Errorf("args: ZoneAssociations[%d]: duplicate Name %q", i, z.Name))
		}

		seen[z.Name] = true

		if z.ZoneID == nil {
			errs = append(errs, fmt.Errorf("args: ZoneAssociations[%d] (%q): ZoneID is nil", i, z.Name))
		}

		if z.VPCID == nil {
			errs = append(errs, fmt.Errorf("args: ZoneAssociations[%d] (%q): VPCID is nil", i, z.Name))
		}

		if z.Region == "" {
			errs = append(errs, fmt.Errorf("args: ZoneAssociations[%d] (%q): Region is empty", i, z.Name))
		}

		if z.CrossAccount && (z.ZoneProvider == nil || z.VPCProvider == nil) {
			errs = append(errs, fmt.Errorf("args: ZoneAssociations[%d] (%q): a cross-account association needs ZoneProvider and VPCProvider", i, z.Name))
		}
	}

	return errs
}

// Validate reports every problem with args at once, or returns nil.
func (a *Args) Validate() error {
	var errs []error

	if a.RequesterVPCID == nil {
		errs = append(errs, errors.New("args: RequesterVPCID is nil"))
	}

	if a.AccepterVPCID == nil {
		errs = append(errs, errors.New("args: AccepterVPCID is nil"))
	}

	if a.AccepterAccountID == "" {
		errs = append(errs, errors.New("args: AccepterAccountID is empty"))
	}

	if a.RequesterProvider == nil {
		errs = append(errs, errors.New("args: RequesterProvider is nil"))
	}

	if a.AccepterProvider == nil {
		errs = append(errs, errors.New("args: AccepterProvider is nil"))
	}

	errs = append(errs, a.checkCIDRs()...)
	errs = append(errs, a.checkRouteTables("Requester", a.RequesterRouteTables)...)
	errs = append(errs, a.checkRouteTables("Accepter", a.AccepterRouteTables)...)
	errs = append(errs, a.checkZones()...)
	errs = append(errs, a.checkNames("")...)

	return errors.Join(errs...)
}

// plan lists every child the args register, in registration order.
func (a *Args) plan(component string) []Child {
	out := []Child{{Component: component, Kind: KindConnection}, {Component: component, Kind: KindAccepter}}

	families := []Family{IPv4}
	if a.RequesterIPv6CIDR != nil && a.AccepterIPv6CIDR != nil {
		families = append(families, IPv6)
	}

	for _, f := range families {
		for _, k := range sortedKeys(a.RequesterRouteTables) {
			out = append(out, Child{Component: component, Kind: KindRoute, Side: Requester, Family: f, Key: k})
		}

		for _, k := range sortedKeys(a.AccepterRouteTables) {
			out = append(out, Child{Component: component, Kind: KindRoute, Side: Accepter, Family: f, Key: k})
		}
	}

	if a.RequesterDNSResolution != nil {
		out = append(out, Child{Component: component, Kind: KindDNSOptions, Side: Requester})
	}

	if a.AccepterDNSResolution != nil {
		out = append(out, Child{Component: component, Kind: KindDNSOptions, Side: Accepter})
	}

	for _, z := range a.ZoneAssociations {
		if z.CrossAccount {
			out = append(out, Child{Component: component, Kind: KindZoneAuthorization, Key: z.Name})
		}

		out = append(out, Child{Component: component, Kind: KindZoneAssociation, Key: z.Name})
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
	s := string(c.Kind)
	if c.Side != "" {
		s += " " + string(c.Side)
	}

	if c.Family != "" {
		s += " " + string(c.Family)
	}

	if c.Key != "" {
		s += " " + c.Key
	}

	return s
}

// New registers the component and its children. It returns an error,
// registering nothing, when args.Validate does or when Names returns an empty
// or repeated name.
//
// The providers come from Args, not from pulumi.Providers: each child is
// registered with the provider of the side it belongs to.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*VpcPeering, error) {
	if args == nil {
		return nil, errors.New("vpcpeering: args is nil")
	}

	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("vpcpeering %s: %w", name, err)
	}

	if err := errors.Join(args.checkNames(name)...); err != nil {
		return nil, fmt.Errorf("vpcpeering %s: %w", name, err)
	}

	names := args.Names
	if names == nil {
		names = DefaultName
	}

	comp := &VpcPeering{}
	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	b := &builder{ctx: ctx, comp: comp, args: args, name: name, names: names}
	if err := b.build(); err != nil {
		return nil, err
	}

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{"connectionId": comp.ConnectionID}); err != nil {
		return nil, err
	}

	return comp, nil
}

type builder struct {
	ctx   *pulumi.Context
	comp  *VpcPeering
	args  *Args
	name  string
	names NameFunc

	connection *ec2.VpcPeeringConnection
	accepter   *ec2.VpcPeeringConnectionAccepter
}

func (b *builder) child(c Child) string {
	c.Component = b.name

	return b.names(c)
}

// childOpts is what every child registers with: the component as parent, the
// provider of its side, and the adoption alias when asked for.
func (b *builder) childOpts(p pulumi.ProviderResource, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := append([]pulumi.ResourceOption{pulumi.Parent(b.comp), pulumi.Provider(p)}, extra...)

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

func (b *builder) tags() pulumi.StringMap {
	if len(b.args.Tags) == 0 {
		return nil
	}

	m := pulumi.StringMap{}
	for k, v := range b.args.Tags {
		m[k] = pulumi.String(v)
	}

	return m
}

func (b *builder) build() error {
	if err := b.connections(); err != nil {
		return err
	}

	if err := b.routes(); err != nil {
		return err
	}

	if err := b.dnsOptions(); err != nil {
		return err
	}

	return b.zones()
}

func (b *builder) connections() error {
	a := b.args

	connArgs := &ec2.VpcPeeringConnectionArgs{
		VpcId:       a.RequesterVPCID,
		PeerVpcId:   a.AccepterVPCID,
		PeerOwnerId: pulumi.String(a.AccepterAccountID),
		// The accepter resource accepts it. AutoAccept on the requester only
		// works within one account and region.
		AutoAccept: pulumi.Bool(false),
		Tags:       b.tags(),
	}
	if a.AccepterRegion != "" {
		connArgs.PeerRegion = pulumi.String(a.AccepterRegion)
	}

	conn, err := ec2.NewVpcPeeringConnection(b.ctx, b.child(Child{Kind: KindConnection}), connArgs,
		b.childOpts(a.RequesterProvider, b.protectOpt()...)...)
	if err != nil {
		return fmt.Errorf("create peering connection: %w", err)
	}

	acc, err := ec2.NewVpcPeeringConnectionAccepter(b.ctx, b.child(Child{Kind: KindAccepter}), &ec2.VpcPeeringConnectionAccepterArgs{
		VpcPeeringConnectionId: conn.ID(),
		AutoAccept:             pulumi.Bool(true),
		Tags:                   b.tags(),
	}, b.childOpts(a.AccepterProvider, b.protectOpt()...)...)
	if err != nil {
		return fmt.Errorf("accept peering connection: %w", err)
	}

	b.connection, b.accepter = conn, acc
	b.comp.ConnectionID = conn.ID().ToStringOutput()

	return nil
}

func (b *builder) routes() error {
	a := b.args

	type side struct {
		side     Side
		tables   map[string]pulumi.StringInput
		provider pulumi.ProviderResource
		peerV4   string
		peerV6   pulumi.StringInput
	}

	sides := []side{
		{Requester, a.RequesterRouteTables, a.RequesterProvider, a.AccepterCIDR, a.AccepterIPv6CIDR},
		{Accepter, a.AccepterRouteTables, a.AccepterProvider, a.RequesterCIDR, a.RequesterIPv6CIDR},
	}

	families := []Family{IPv4}
	if a.RequesterIPv6CIDR != nil && a.AccepterIPv6CIDR != nil {
		families = append(families, IPv6)
	}

	for _, f := range families {
		for _, s := range sides {
			for _, k := range sortedKeys(s.tables) {
				r := &ec2.RouteArgs{
					RouteTableId:           s.tables[k],
					VpcPeeringConnectionId: b.connection.ID(),
				}

				if f == IPv4 {
					r.DestinationCidrBlock = pulumi.String(s.peerV4)
				} else {
					r.DestinationIpv6CidrBlock = s.peerV6
				}

				// The accepter must have accepted before a route may use the
				// connection.
				if _, err := ec2.NewRoute(b.ctx, b.child(Child{Kind: KindRoute, Side: s.side, Family: f, Key: k}), r,
					b.childOpts(s.provider, pulumi.DependsOn([]pulumi.Resource{b.accepter}))...); err != nil {
					return fmt.Errorf("add %s %s route %s: %w", s.side, f, k, err)
				}
			}
		}
	}

	return nil
}

func (b *builder) dnsOptions() error {
	a := b.args

	for _, o := range []struct {
		side     Side
		on       *bool
		provider pulumi.ProviderResource
	}{
		{Requester, a.RequesterDNSResolution, a.RequesterProvider},
		{Accepter, a.AccepterDNSResolution, a.AccepterProvider},
	} {
		if o.on == nil {
			continue
		}

		opts := &ec2.PeeringConnectionOptionsArgs{VpcPeeringConnectionId: b.connection.ID()}
		blk := &ec2.PeeringConnectionOptionsRequesterArgs{AllowRemoteVpcDnsResolution: pulumi.Bool(*o.on)}
		accBlk := &ec2.PeeringConnectionOptionsAccepterArgs{AllowRemoteVpcDnsResolution: pulumi.Bool(*o.on)}

		if o.side == Requester {
			opts.Requester = blk
		} else {
			opts.Accepter = accBlk
		}

		if _, err := ec2.NewPeeringConnectionOptions(b.ctx, b.child(Child{Kind: KindDNSOptions, Side: o.side}), opts,
			b.childOpts(o.provider, pulumi.DependsOn([]pulumi.Resource{b.accepter}))...); err != nil {
			return fmt.Errorf("set %s DNS resolution: %w", o.side, err)
		}
	}

	return nil
}

func (b *builder) zones() error {
	for _, z := range b.args.ZoneAssociations {
		zoneProvider, vpcProvider := z.ZoneProvider, z.VPCProvider
		if !z.CrossAccount {
			// One account: the zone's provider (or the VPC's, when only that
			// is given) creates the single association.
			if vpcProvider == nil {
				vpcProvider = zoneProvider
			}

			if vpcProvider == nil {
				vpcProvider = b.args.RequesterProvider
			}
		}

		assoc := &route53.ZoneAssociationArgs{
			ZoneId:    z.ZoneID,
			VpcId:     z.VPCID,
			VpcRegion: pulumi.String(z.Region),
		}

		var extra []pulumi.ResourceOption

		if z.CrossAccount {
			// The zone's owner authorizes the VPC before the VPC's owner may
			// associate it.
			auth, err := route53.NewVpcAssociationAuthorization(b.ctx, b.child(Child{Kind: KindZoneAuthorization, Key: z.Name}),
				&route53.VpcAssociationAuthorizationArgs{
					ZoneId:    z.ZoneID,
					VpcId:     z.VPCID,
					VpcRegion: pulumi.String(z.Region),
				}, b.childOpts(zoneProvider)...)
			if err != nil {
				return fmt.Errorf("authorize zone association %s: %w", z.Name, err)
			}

			extra = append(extra, pulumi.DependsOn([]pulumi.Resource{auth}))
		}

		if _, err := route53.NewZoneAssociation(b.ctx, b.child(Child{Kind: KindZoneAssociation, Key: z.Name}), assoc,
			b.childOpts(vpcProvider, extra...)...); err != nil {
			return fmt.Errorf("create zone association %s: %w", z.Name, err)
		}
	}

	return nil
}
