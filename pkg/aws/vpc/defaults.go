package vpc

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// defaults hardens the VPC's default security group and default network ACL.
func (b *builder) defaults() error {
	a := b.args

	if !a.KeepDefaultSecurityGroup {
		c := Child{Kind: KindDefaultSecurityGroup}

		if _, err := ec2.NewDefaultSecurityGroup(b.ctx, b.child(c), &ec2.DefaultSecurityGroupArgs{
			VpcId:   b.vpc.ID(),
			Ingress: ec2.DefaultSecurityGroupIngressArray{},
			Egress:  ec2.DefaultSecurityGroupEgressArray{},
			Tags:    b.tags(c, nil, nil),
		}, b.childOpts()...); err != nil {
			return fmt.Errorf("restrict default security group: %w", err)
		}
	}

	if a.KeepDefaultNACL {
		return nil
	}

	c := Child{Kind: KindDefaultNACL}

	if _, err := ec2.NewDefaultNetworkAcl(b.ctx, b.child(c), &ec2.DefaultNetworkAclArgs{
		DefaultNetworkAclId: b.vpc.DefaultNetworkAclId,
		Ingress:             b.naclIngress(),
		Egress:              b.naclEgress(),
		Tags:                b.tags(c, nil, nil),
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("restrict default network ACL: %w", err)
	}

	return nil
}

// naclIngress is the default network ACL's inbound rules.
//
//	100: all traffic from the VPC (IPv4)
//	101: all traffic from the VPC (IPv6)
//	110-113: ephemeral TCP return traffic from anywhere, split around 3389
//	114-119: HTTPS (443) from each DefaultNACLHTTPSIngress CIDR
//	120-121: DNS (53/UDP) return traffic
//	130: NTP (123/UDP) return traffic
//	140-143: ephemeral UDP return traffic, split around 3389
//	160-179: TCP from each DefaultNACLPeerIngress entry's CIDR to its port
//
// Everything else is denied by the ACL's implicit final rule. The ephemeral
// ranges skip RDP (3389) so that no rule admits an admin port from anywhere.
func (b *builder) naclIngress() ec2.DefaultNetworkAclIngressArray {
	a := b.args
	v6 := a.ipv6()

	rules := ec2.DefaultNetworkAclIngressArray{ingressRule(100, "-1", a.CIDR, "", 0, 0)}

	if v6 {
		rules = append(rules, ec2.DefaultNetworkAclIngressArgs{
			RuleNo: pulumi.Int(101), Protocol: pulumi.String("-1"), Action: pulumi.String("allow"),
			FromPort: pulumi.Int(0), ToPort: pulumi.Int(0), Ipv6CidrBlock: b.ipv6CIDR,
		})
	}

	rules = append(rules, ingressRule(110, "6", "0.0.0.0/0", "", 1024, 3388))
	if v6 {
		rules = append(rules, ingressRule(111, "6", "", "::/0", 1024, 3388))
	}

	rules = append(rules, ingressRule(112, "6", "0.0.0.0/0", "", 3390, 65535))
	if v6 {
		rules = append(rules, ingressRule(113, "6", "", "::/0", 3390, 65535))
	}

	for i, cidr := range a.DefaultNACLHTTPSIngress {
		rules = append(rules, ingressRule(114+i, "6", cidr, "", 443, 443))
	}

	rules = append(rules, ingressRule(120, "17", "0.0.0.0/0", "", 53, 53))
	if v6 {
		rules = append(rules, ingressRule(121, "17", "", "::/0", 53, 53))
	}

	rules = append(rules, ingressRule(130, "17", "0.0.0.0/0", "", 123, 123),
		ingressRule(140, "17", "0.0.0.0/0", "", 1024, 3388))
	if v6 {
		rules = append(rules, ingressRule(141, "17", "", "::/0", 1024, 3388))
	}

	rules = append(rules, ingressRule(142, "17", "0.0.0.0/0", "", 3390, 65535))
	if v6 {
		rules = append(rules, ingressRule(143, "17", "", "::/0", 3390, 65535))
	}

	for i, e := range a.DefaultNACLPeerIngress {
		rules = append(rules, ingressRule(160+i, "6", e.CIDR, "", e.Port, e.Port))
	}

	return rules
}

// naclEgress is the default network ACL's outbound rules.
//
//	100-101: all traffic to the VPC (IPv4, IPv6)
//	110-111: HTTPS (443) anywhere
//	120-123: DNS (53, UDP and TCP) anywhere
//	130: NTP (123/UDP) anywhere
//	140-143: ephemeral TCP, split around 3389
//	150-153: ephemeral UDP, split around 3389
//	160-179: TCP to each DefaultNACLPeerEgress entry's CIDR and port
func (b *builder) naclEgress() ec2.DefaultNetworkAclEgressArray {
	a := b.args
	v6 := a.ipv6()

	rules := ec2.DefaultNetworkAclEgressArray{egressRule(100, "-1", a.CIDR, "", 0, 0)}

	if v6 {
		rules = append(rules, ec2.DefaultNetworkAclEgressArgs{
			RuleNo: pulumi.Int(101), Protocol: pulumi.String("-1"), Action: pulumi.String("allow"),
			FromPort: pulumi.Int(0), ToPort: pulumi.Int(0), Ipv6CidrBlock: b.ipv6CIDR,
		})
	}

	rules = append(rules, egressRule(110, "6", "0.0.0.0/0", "", 443, 443))
	if v6 {
		rules = append(rules, egressRule(111, "6", "", "::/0", 443, 443))
	}

	rules = append(rules, egressRule(120, "17", "0.0.0.0/0", "", 53, 53), egressRule(121, "6", "0.0.0.0/0", "", 53, 53))
	if v6 {
		rules = append(rules, egressRule(122, "17", "", "::/0", 53, 53), egressRule(123, "6", "", "::/0", 53, 53))
	}

	rules = append(rules, egressRule(130, "17", "0.0.0.0/0", "", 123, 123))

	for _, p := range []struct {
		base  int
		proto string
	}{{140, "6"}, {150, "17"}} {
		rules = append(rules, egressRule(p.base, p.proto, "0.0.0.0/0", "", 1024, 3388))
		if v6 {
			rules = append(rules, egressRule(p.base+1, p.proto, "", "::/0", 1024, 3388))
		}

		rules = append(rules, egressRule(p.base+2, p.proto, "0.0.0.0/0", "", 3390, 65535))
		if v6 {
			rules = append(rules, egressRule(p.base+3, p.proto, "", "::/0", 3390, 65535))
		}
	}

	for i, e := range a.DefaultNACLPeerEgress {
		rules = append(rules, egressRule(160+i, "6", e.CIDR, "", e.Port, e.Port))
	}

	return rules
}

func ingressRule(ruleNo int, protocol, cidr, ipv6CIDR string, fromPort, toPort int) ec2.DefaultNetworkAclIngressArgs {
	args := ec2.DefaultNetworkAclIngressArgs{
		RuleNo:   pulumi.Int(ruleNo),
		Protocol: pulumi.String(protocol),
		Action:   pulumi.String("allow"),
		FromPort: pulumi.Int(fromPort),
		ToPort:   pulumi.Int(toPort),
	}

	if cidr != "" {
		args.CidrBlock = pulumi.String(cidr)
	}

	if ipv6CIDR != "" {
		args.Ipv6CidrBlock = pulumi.String(ipv6CIDR)
	}

	return args
}

func egressRule(ruleNo int, protocol, cidr, ipv6CIDR string, fromPort, toPort int) ec2.DefaultNetworkAclEgressArgs {
	args := ec2.DefaultNetworkAclEgressArgs{
		RuleNo:   pulumi.Int(ruleNo),
		Protocol: pulumi.String(protocol),
		Action:   pulumi.String("allow"),
		FromPort: pulumi.Int(fromPort),
		ToPort:   pulumi.Int(toPort),
	}

	if cidr != "" {
		args.CidrBlock = pulumi.String(cidr)
	}

	if ipv6CIDR != "" {
		args.Ipv6CidrBlock = pulumi.String(ipv6CIDR)
	}

	return args
}
