package vpcpeering

import "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

// PublicRouteTableKey is the key RouteTables gives the public route table in
// the map Args.RequesterRouteTables and Args.AccepterRouteTables take. The
// private route tables are keyed by availability zone name.
const PublicRouteTableKey = "public"

// RouteTables builds the route table map of one side of a peering from the
// side's VPC outputs: the public route table under PublicRouteTableKey (when
// publicID is not empty) and each private route table under its availability
// zone name. A host in a public subnet (a subnet router, a bastion) reaches
// the peer only if the public table carries the route too, which is why it is
// listed with the private ones.
func RouteTables(publicID string, privateByZone map[string]string) map[string]pulumi.StringInput {
	m := make(map[string]pulumi.StringInput, len(privateByZone)+1)

	if publicID != "" {
		m[PublicRouteTableKey] = pulumi.String(publicID)
	}

	for az, id := range privateByZone {
		m[az] = pulumi.String(id)
	}

	return m
}
