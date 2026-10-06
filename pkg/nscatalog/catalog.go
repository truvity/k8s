// Package nscatalog is the namespace catalog: one row per (cluster, namespace)
// with one column group per concern (guardrails, identity, data, exposure,
// observability, delivery). A consumer generates the catalog from its own
// facts, writes it as YAML (Marshal, Unmarshal, MarshalUnder) and can check it
// against a live cluster (Compare, CompareACKSelectors).
//
// The catalog maps the CONSUMERS of a concern feature (which namespace gets
// it). It describes what the renderers already produce, so it can be checked
// against the live clusters; the per-project Applications of a cluster read it.
package nscatalog

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Kinds of namespace.
const (
	// KindSystem is a namespace Kubernetes itself creates.
	KindSystem = "system"
	// KindPlatform is a namespace of a platform component (L1/L2).
	KindPlatform = "platform"
	// KindProduct is a business project's namespace on a workload cluster.
	KindProduct = "product"
	// KindCI is a CI tenant namespace (ci, ci-<org>-<repo>).
	KindCI = "ci"
	// KindEmployee is an employee tenant namespace (emp-<slug>).
	KindEmployee = "employee"
	// KindKargoProject is a Kargo Project's namespace on the management cluster.
	KindKargoProject = "kargo-project"
)

// Writers: who creates the Namespace object (and, unless the Pod Security
// column says otherwise, stamps its labels).
const (
	WriterSystem        = "system"         // the Kubernetes distribution
	WriterFoundation    = "foundation"     // the <c>-foundation Application (cluster-foundation chart)
	WriterTenants       = "tenants"        // the <c>-tenants Application (tenancy chart)
	WriterPulumiTenancy = "pulumi-tenancy" // the <c>/tenancy Pulumi stack (labels on a pre-made namespace)
	WriterKargo         = "kargo"          // the Kargo controller (Project namespaces)
	WriterComponent     = "component"      // the component's own Application
	// WriterGuardrailsProjects is the target shape's <c>-guardrails-projects
	// Application which owns every project, CI and employee
	// namespace from its catalog row.
	WriterGuardrailsProjects = "guardrails-projects"
)

// Pod Security label writers.
const (
	// LabelWriterOwner: the Namespace's writer stamps the PSA labels.
	LabelWriterOwner = "owner"
	// LabelWriterClusterBaseline: the cluster-baseline Application labels a
	// namespace nothing else renders.
	LabelWriterClusterBaseline = "cluster-baseline"
)

type (
	// Catalog is one cluster's namespace catalog.
	Catalog struct {
		Cluster    string `yaml:"cluster"`
		Namespaces []Row  `yaml:"namespaces"`
	}

	// Row is one namespace on one cluster.
	Row struct {
		Name string `yaml:"name"`
		Kind string `yaml:"kind"`
		// Project is the business project the namespace belongs to
		// (product rows, and the Kargo Project namespace of a business
		// project on the management cluster).
		Project       string        `yaml:"project,omitempty"`
		Guardrails    Guardrails    `yaml:"guardrails"`
		Identity      Identity      `yaml:"identity,omitempty"`
		Data          Data          `yaml:"data,omitempty"`
		Exposure      Exposure      `yaml:"exposure,omitempty"`
		Observability Observability `yaml:"observability,omitempty"`
		Delivery      Delivery      `yaml:"delivery,omitempty"`
		// Labels are the platform-managed labels the live Namespace must
		// carry: every *.truvity.io/*, truvity.com/* and
		// pod-security.kubernetes.io/* key, and nothing else.
		Labels map[string]string `yaml:"labels,omitempty"`
	}

	// Guardrails is the guardrails concern: the namespace object, its Pod
	// Security level, the tenant baseline and the quota.
	Guardrails struct {
		Writer      string       `yaml:"writer"`
		PodSecurity *PodSecurity `yaml:"podSecurity,omitempty"`
		// TenantBaseline: the tenant-baseline NetworkPolicy and the role
		// spine RoleBindings (tenancy chart or its Pulumi twin).
		TenantBaseline bool `yaml:"tenantBaseline,omitempty"`
		// Quota is the tenancy profile's ResourceQuota and LimitRange
		// (ci, employee); empty for none.
		Quota string `yaml:"quota,omitempty"`
		// Deletable: removing the row deletes the namespace (no
		// Prune=false,Delete=false guard); employees only.
		Deletable bool `yaml:"deletable,omitempty"`
		// RoleBindings are the namespace's role spine: who may deploy,
		// view or own it.
		RoleBindings []RoleBinding `yaml:"roleBindings,omitempty"`
	}

	// RoleBinding is one binding of the role spine.
	RoleBinding struct {
		Name        string    `yaml:"name"`
		ClusterRole string    `yaml:"clusterRole"`
		Subjects    []Subject `yaml:"subjects"`
	}

	// Subject is one RBAC subject.
	Subject struct {
		Kind string `yaml:"kind"`
		Name string `yaml:"name"`
	}

	// PodSecurity is the namespace's Pod Security Admission row.
	PodSecurity struct {
		Level   string   `yaml:"level"`
		Version string   `yaml:"version"`
		Modes   []string `yaml:"modes,flow"`
		Reason  string   `yaml:"reason,omitempty"`
		// LabelWriter is who stamps the labels: owner or cluster-baseline.
		LabelWriter string `yaml:"labelWriter"`
	}

	// Identity is the identity concern.
	Identity struct {
		// ACKCeiling: the project declares AWS capabilities on this
		// cluster, so Pulumi mints <c>-ack-project-<p> and an
		// IAMRoleSelector must bind it to this namespace.
		ACKCeiling bool `yaml:"ackCeiling,omitempty"`
		// ACKSelectorWriter is who renders that IAMRoleSelector today
		// (pulumi-tenancy), or "none" where nothing does.
		ACKSelectorWriter string `yaml:"ackSelectorWriter,omitempty"`
		// WorkloadIdentity: the namespace is admitted to the
		// truvity-identity-ca Bundle and may mint SPIFFE leaves.
		WorkloadIdentity bool `yaml:"workloadIdentity,omitempty"`
		// PodIdentities are the shared test identities a CI or employee
		// namespace gets a ServiceAccount of each name, bound
		// by an ACK PodIdentityAssociation to <cluster>-<name>.
		PodIdentities []string `yaml:"podIdentities,omitempty,flow"`
	}

	// Data is the data concern.
	Data struct {
		// CNPGMetrics: the cnpg-platform Application renders the
		// tenant-cnpg-metrics NetworkPolicy for the namespace.
		CNPGMetrics bool `yaml:"cnpgMetrics,omitempty"`
		// NATS is "account" where the shared broker has an account of the
		// namespace's name.
		NATS string `yaml:"nats,omitempty"`
	}

	// Exposure is the exposure concern.
	Exposure struct {
		// Listeners are the ListenerSet groups that
		// admit the namespace's routes by name.
		Listeners []string `yaml:"listeners,omitempty,flow"`
		// RouteGrantBusiness: the namespace carries the coarse
		// gateway.truvity.io/route-grant-business-origin label, which only
		// the multi-project wildcard listener selects.
		RouteGrantBusiness bool `yaml:"routeGrantBusiness,omitempty"`
	}

	// Observability is the observability concern.
	Observability struct {
		// Owner is the company stamped on the namespace's telemetry
		// (observability-emitters tenancy.owners).
		Owner string `yaml:"owner"`
	}

	// Delivery is the delivery concern.
	Delivery struct {
		// AppProject is the ArgoCD AppProject (on the management
		// cluster) whose destination is this namespace.
		AppProject string `yaml:"appProject,omitempty"`
	}
)

// Row returns the named row, or nil.
func (c *Catalog) Row(name string) *Row {
	for i := range c.Namespaces {
		if c.Namespaces[i].Name == name {
			return &c.Namespaces[i]
		}
	}

	return nil
}

// Names returns the namespace names, sorted.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.Namespaces))
	for i := range c.Namespaces {
		out = append(out, c.Namespaces[i].Name)
	}

	slices.Sort(out)

	return out
}

// Marshal renders the catalog as YAML behind header (the provenance comment
// the caller stamps on line 1; empty for none): rows sorted by name, map keys
// sorted.
func Marshal(c *Catalog, header string) ([]byte, error) {
	sorted := *c
	sorted.Namespaces = slices.Clone(c.Namespaces)
	slices.SortFunc(sorted.Namespaces, func(a, b Row) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})

	var buf bytes.Buffer

	buf.WriteString(header)

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(&sorted); err != nil {
		return nil, fmt.Errorf("encode namespace catalog %s: %w", c.Cluster, err)
	}

	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode namespace catalog %s: %w", c.Cluster, err)
	}

	return buf.Bytes(), nil
}

// Unmarshal parses a catalog file.
func Unmarshal(data []byte) (*Catalog, error) {
	var c Catalog
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse namespace catalog: %w", err)
	}

	return &c, nil
}

// MarshalUnder renders the catalog as the value of one top-level key (no
// header), for embedding in a Helm values file.
func MarshalUnder(key string, c *Catalog) ([]byte, error) {
	data, err := Marshal(c, "")
	if err != nil {
		return nil, err
	}

	body := string(data)

	var b strings.Builder

	b.WriteString(key + ":\n")

	for _, line := range strings.SplitAfter(body, "\n") {
		if line == "" {
			continue
		}

		if line == "\n" {
			b.WriteString(line)

			continue
		}

		b.WriteString("  " + line)
	}

	return []byte(b.String()), nil
}
