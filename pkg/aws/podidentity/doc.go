// Package podidentity deploys one EKS Pod Identity role as a Pulumi
// ComponentResource: the IAM role whose trust policy admits the EKS Pod
// Identity service for one namespace and its service accounts, the
// permissions it carries, and one PodIdentityAssociation per service account.
//
// A role and its associations are one unit because the trust policy is what
// binds the role to those service accounts: a role without its association
// grants nothing, and an association without that trust fails at assume time.
// One component is therefore one role and the associations that use it.
//
// The permissions are the caller's. The component takes an inline role policy
// (InlinePolicy), a managed policy it creates and attaches (ManagedPolicy), or
// both; it does not know what the role is for.
//
// What the component refuses, before registering anything: a missing provider,
// cluster, role name, namespace or service account, a repeated service
// account, a trust policy it was given no way to render (neither TrustPolicy
// nor the account and cluster ARN), a permission with no name or document,
// and a naming hook that returns an empty or repeated name. All problems are
// reported at once.
//
// The default trust policy is always scoped to the cluster (the source
// account and the source ARN) and to the namespace and service accounts. A
// trust that is not (for example one that admits a whole cluster) can only be
// supplied through Args.TrustPolicy, which is used verbatim.
//
// Nothing is protected unless Args.Protect says so.
//
// See docs/reference.md for the children, their names and their aliases.
package podidentity
