package cluster

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
)

// Provider names the kind of component that provisioned a cluster. It is
// informational: a consumer that branches on it is reaching past the
// contract, and should ask for a Capability instead.
type Provider string

const (
	// ProviderEKS is an Amazon EKS cluster in Auto Mode.
	ProviderEKS Provider = "eks"
	// ProviderTalos is a self-hosted Talos Linux cluster.
	ProviderTalos Provider = "talos"
)

// Capability is an optional ability a cluster may offer. A consumer asks
// whether a capability is present; it does not infer it from the Provider.
type Capability string

const (
	// NodePools means node capacity can be declared in groups with their own
	// shape (instance class, labels, taints) and scales on demand.
	NodePools Capability = "node-pools"
	// Storage means a default StorageClass exists and provisions persistent
	// volumes dynamically.
	Storage Capability = "storage"
	// NetworkPolicy means NetworkPolicy objects are enforced, not merely
	// accepted.
	NetworkPolicy Capability = "network-policy"
	// WorkloadIdentity means a pod's ServiceAccount can be exchanged for
	// credentials outside the cluster; the OIDC issuer is set.
	WorkloadIdentity Capability = "workload-identity"
	// LoadBalancing means a Service of type LoadBalancer, or a Gateway, is
	// given an externally reachable address.
	LoadBalancing Capability = "load-balancing"
	// APIAccess means the API server is reachable from the places the caller
	// named (a private range, a peered network, a public endpoint), and
	// Outputs.Endpoint is the address to use from there.
	APIAccess Capability = "api-access"
)

// All lists every capability this version of the contract defines, in a
// stable order.
func All() []Capability {
	return []Capability{NodePools, Storage, NetworkPolicy, WorkloadIdentity, LoadBalancing, APIAccess}
}

// Capabilities is a set of Capability.
type Capabilities map[Capability]struct{}

// NewCapabilities returns the set holding exactly the given capabilities.
func NewCapabilities(caps ...Capability) Capabilities {
	s := make(Capabilities, len(caps))
	for _, c := range caps {
		s[c] = struct{}{}
	}
	return s
}

// Has reports whether c is in the set.
func (s Capabilities) Has(c Capability) bool {
	_, ok := s[c]
	return ok
}

// List returns the members in the order of All, followed by any this
// version does not know (which Validate refuses).
func (s Capabilities) List() []Capability {
	out := make([]Capability, 0, len(s))
	for _, c := range All() {
		if s.Has(c) {
			out = append(out, c)
		}
	}
	var extra []Capability
	for c := range s {
		if !known(c) {
			extra = append(extra, c)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return append(out, extra...)
}

func known(c Capability) bool {
	for _, k := range All() {
		if k == c {
			return true
		}
	}
	return false
}

// Outputs is what a provider component reports about a cluster it
// provisioned.
type Outputs struct {
	// Provider is the kind of component that made the cluster.
	Provider Provider
	// Name is the cluster's name, as the caller supplied it: a DNS-1123
	// label. Nothing here derives it.
	Name string
	// Endpoint is the API server's address, an https URL.
	Endpoint string
	// CertificateAuthorityPEM is the PEM bundle a client trusts the API
	// server against. It is the PEM text itself, not its base64 form.
	CertificateAuthorityPEM string
	// OIDCIssuer is the https URL that signs ServiceAccount tokens. It is
	// required when WorkloadIdentity is offered and empty otherwise.
	OIDCIssuer string
	// Capabilities is what the cluster offers beyond the four facts
	// above.
	Capabilities Capabilities
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Validate refuses an Outputs a consumer could not safely use. It reports
// every problem, not the first.
func (o Outputs) Validate() error {
	var errs []error

	if !dnsLabel.MatchString(o.Name) {
		errs = append(errs, fmt.Errorf("name %q is not a DNS-1123 label", o.Name))
	}
	if err := requireHTTPS("endpoint", o.Endpoint); err != nil {
		errs = append(errs, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(o.CertificateAuthorityPEM)) {
		errs = append(errs, errors.New("certificateAuthorityPEM holds no PEM certificate"))
	}
	for _, c := range o.Capabilities.List() {
		if !known(c) {
			errs = append(errs, fmt.Errorf("unknown capability %q", c))
		}
	}
	switch {
	case o.Capabilities.Has(WorkloadIdentity):
		if err := requireHTTPS("oidcIssuer", o.OIDCIssuer); err != nil {
			errs = append(errs, fmt.Errorf("workload-identity is offered but %w", err))
		}
	case o.OIDCIssuer != "":
		errs = append(errs, errors.New("oidcIssuer is set but workload-identity is not offered"))
	}
	return errors.Join(errs...)
}

func requireHTTPS(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s %q is not an https URL", field, raw)
	}
	return nil
}
