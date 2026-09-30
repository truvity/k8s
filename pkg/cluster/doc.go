// Package cluster is the provider-neutral contract of a Kubernetes cluster
// that some provider component (EKS Auto Mode, self-hosted Talos, ...) has
// provisioned.
//
// A consumer of a cluster — a chart installer, an access layer, a backup
// job — needs a small, stable set of facts: what the cluster is called, how
// to reach its API, what to trust when it does, who issues its workload
// identities, and which optional abilities it has. Outputs carries exactly
// those, and Validate refuses an Outputs that would mislead a consumer, so
// every provider is held to the same shape by the same code.
//
// The package has no cloud dependency and no estate particulars: a name, an
// endpoint and an issuer are values the provider component reports, never
// values this package chooses. See docs/decisions/0001-provider-contract.md.
package cluster
