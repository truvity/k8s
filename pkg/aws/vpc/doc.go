// Package vpc deploys one AWS VPC with its subnets, route tables, gateways,
// default-resource hardening, gateway endpoints and flow logs as one Pulumi
// ComponentResource.
//
// The caller states the layout: the VPC's IPv4 CIDR, the availability zones
// it spans, and each logical subnet with one CIDR per zone. The component
// adds what every such VPC needs: an internet gateway and a public route
// table when there is a public subnet, one private route table per zone,
// IPv6 (an Amazon-provided /56, a /64 per subnet and zone, an egress-only
// gateway for the private route tables), NAT egress for the private route
// tables (one gateway, one per zone, or none), S3 and DynamoDB gateway
// endpoints, an emptied default security group, a restrictive default
// network ACL, and flow logs to CloudWatch Logs.
//
// Every default is a field of Args with a documented value; every child's
// logical name comes from a caller-supplied hook, and every child's tags can
// be extended per child. Nothing about an estate is built in.
//
// What the component refuses, before registering anything: a bad or
// non-IPv4 VPC CIDR, subnets that fall outside it or overlap each other, a
// subnet that does not span exactly the listed zones, IPv6 indexes that
// collide or do not fit a /56, a NAT with no public subnet to sit in, a
// missing provider, and a naming hook that returns an empty or repeated
// name. All problems are reported at once.
//
// The VPC and its subnets are protected unless Args.Protect points at
// false: replacing either destroys everything that lives in them.
//
// The default network ACL is managed with a DefaultNetworkAcl resource,
// which replaces the ACL's whole rule list on every apply. The component is
// therefore the sole owner of that list: a rule added elsewhere (a
// NetworkAclRule against the same ACL) is deleted by the next apply. Admit
// extra traffic with Args.DefaultNACLHTTPSIngress, or leave the default ACL
// alone with Args.KeepDefaultNACL and own it yourself.
//
// See docs/reference.md for the children, their names and their aliases.
package vpc
