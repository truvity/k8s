// Package eksoidc points an EKS cluster's API server at an external OIDC
// issuer, as a Pulumi ComponentResource: one eks.IdentityProviderConfig, the
// association EKS validates bearer tokens against.
//
// The caller states the cluster, the issuer URL (https), the client id (the
// audience of every token the cluster accepts) and the association's name. The
// claims default to what an issuer that puts a stable subject and group names
// in its tokens needs: the username from "sub" with no prefix ("-", EKS's
// value for none) and the groups from "groups". Args.UsernameClaim,
// Args.UsernamePrefix and Args.GroupsClaim replace them.
//
// EKS admits ONE external OIDC association per cluster, so the child is
// registered with DeleteBeforeReplace: a replace disassociates the old one
// first. Replacing it is a full cluster update of 10 to 15 minutes during
// which OIDC sign-in is down, so the association is protected unless
// Args.Protect points at false.
//
// What the component refuses, before registering anything: a missing provider
// or cluster name, an empty client id, association name or issuer URL, an
// issuer URL that is not https, and a naming hook that returns an empty name.
// All problems are reported at once.
//
// See docs/reference.md for the child, its name and its alias.
package eksoidc
