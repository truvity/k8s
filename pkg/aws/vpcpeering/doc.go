// Package vpcpeering deploys one VPC peering connection between two VPCs, with
// the routes that make it carry traffic and, optionally, the private
// hosted-zone associations that make names resolve across it, as one Pulumi
// ComponentResource.
//
// The two sides may be in different accounts and regions: the caller supplies
// one AWS provider per side. The component creates the connection from the
// requester side, accepts it from the accepter side, adds a route to the
// other VPC's CIDR in every route table the caller lists on each side (IPv6
// too when both VPCs' IPv6 CIDRs are given), and can set the DNS resolution
// options of either side.
//
// What the component refuses, before registering anything: overlapping IPv4
// CIDRs (AWS refuses the peering, and a route cannot tell the two apart), a
// missing provider, a side with no route table, and a half-specified IPv6
// pair.
//
// What it deliberately does not do: it creates no network ACL rules and no
// security group rules. A caller that owns a VPC's default network ACL owns
// its whole rule list, and a rule added here would be wiped by the next apply
// of that owner. Admit cross-VPC traffic where the ACL and the groups are
// defined.
//
// The connection is protected unless Args.Protect points at false: deleting
// it cuts all traffic between the VPCs.
//
// See docs/reference.md for the children, their names and their aliases.
package vpcpeering
