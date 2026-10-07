// Package ekscluster deploys one EKS Auto Mode cluster as a Pulumi
// ComponentResource: the cluster, the KMS key its secrets are encrypted with,
// the cluster's IAM role and the Auto Mode node role with their policies, and
// the CloudWatch log group the control plane logs to.
//
// The shape is fixed on purpose: API authentication mode, no bootstrap
// creator-admin, no self-managed add-ons, secrets encrypted with a rotated
// customer-managed key, control-plane logs on, and EKS-managed compute,
// load balancing and block storage. What the caller states is what varies:
// the cluster's name and Kubernetes version, its subnets, its service CIDR
// (create-time immutable), the managed policies on each role, and any inline
// policies on the node role. The trust documents default to the EKS service
// (cluster role) and the EC2 service (node role); Args.ClusterTrustPolicy and
// Args.NodeTrustPolicy replace them verbatim.
//
// Args.AccessEntries are the principals given access through the API
// authentication mode (one access entry and one access policy association
// each), and Args.CoreDNS installs the coredns add-on with a Corefile the
// component renders from the stock file plus the caller's extra lines.
// Security group rules, DNS records and other add-ons are not part of the
// component: they belong to whoever owns the cluster and take the cluster
// from the component's Cluster field.
//
// What the component refuses, before registering anything: a missing provider,
// name, version, subnet or service CIDR, a service CIDR that is not IPv4, an
// empty or repeated subnet, policy ARN or inline policy, a cluster role with no
// policy, one role name given to both roles, an unknown control-plane log type,
// a key rotation period outside what KMS accepts, an access entry with no
// name, principal or policy or a repeated name or principal, a CoreDNS version
// with no stock Corefile of its own, an extra Corefile line that is empty or
// spans lines, and a naming hook that returns an empty or repeated name. All
// problems are reported at once.
//
// EksCluster.Contract reports the cluster under the provider-neutral contract
// of pkg/cluster. Args.UpgradePolicy and Args.DeletionProtection are sent only
// when set, so adopting a cluster with neither changes nothing.
//
// The cluster, the KMS key and the two roles are protected unless Args.Protect
// points at false: replacing any of them is an outage or a loss of the key
// that decrypts the cluster's secrets.
//
// See docs/reference.md for the children, their names and their aliases.
package ekscluster
