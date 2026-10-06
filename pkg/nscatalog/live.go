package nscatalog

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Mismatch kinds.
const (
	// MissingLive: the catalog has a row, the cluster has no such namespace.
	MissingLive = "missing-live"
	// NotInCatalog: the cluster has a namespace the catalog has no row for.
	NotInCatalog = "not-in-catalog"
	// LabelMissing: the catalog expects a label the live namespace lacks.
	LabelMissing = "label-missing"
	// LabelExtra: the live namespace carries a managed label the catalog
	// does not expect.
	LabelExtra = "label-extra"
	// LabelDiffers: both carry the label with different values.
	LabelDiffers = "label-differs"
	// ACKSelectorMissing: the row has an ACK ceiling (identity.ackCeiling)
	// and no live IAMRoleSelector names the namespace, so its ACK objects
	// reconcile under the controllers' default roles, outside the ceiling.
	ACKSelectorMissing = "ack-selector-missing"
	// ACKSelectorUnexpected: a live IAMRoleSelector names a namespace whose
	// row has no ACK ceiling.
	ACKSelectorUnexpected = "ack-selector-unexpected"
)

type (
	// LiveNamespace is one namespace as the API server reports it.
	LiveNamespace struct {
		Name   string
		Labels map[string]string
	}

	// IAMRoleSelector is one live ACK IAMRoleSelector: its name and the
	// namespaces it binds by name (spec.namespaceSelector.names).
	IAMRoleSelector struct {
		Name       string
		Namespaces []string
	}

	// Mismatch is one difference between the catalog and a live cluster.
	Mismatch struct {
		Cluster   string
		Namespace string
		Kind      string
		// Label is the label key, for the label kinds.
		Label string
		// Want and Got are the catalog's and the live value.
		Want, Got string
	}
)

func (m Mismatch) String() string {
	switch m.Kind {
	case LabelMissing, LabelExtra, LabelDiffers:
		return fmt.Sprintf("%s/%s: %s %s (catalog %q, live %q)", m.Cluster, m.Namespace, m.Kind, m.Label, m.Want, m.Got)
	case ACKSelectorUnexpected:
		return fmt.Sprintf("%s/%s: %s (IAMRoleSelector %s)", m.Cluster, m.Namespace, m.Kind, m.Got)
	default:
		return fmt.Sprintf("%s/%s: %s", m.Cluster, m.Namespace, m.Kind)
	}
}

// ManagedByDomains returns the managed-label predicate Compare takes: a key is
// managed when its prefix is a Pod Security label or one of the domains (or a
// subdomain of one). Everything else (kubernetes.io/metadata.name, Argo CD and
// Kargo bookkeeping, Helm) is not the catalog's.
func ManagedByDomains(domains ...string) func(key string) bool {
	return func(key string) bool {
		prefix, _, ok := strings.Cut(key, "/")
		if !ok {
			return false
		}

		if prefix == "pod-security.kubernetes.io" {
			return true
		}

		for _, d := range domains {
			if prefix == d || strings.HasSuffix(prefix, "."+d) {
				return true
			}
		}

		return false
	}
}

// ParseKubectlNamespaces reads `kubectl get namespaces -o json`.
func ParseKubectlNamespaces(data []byte) ([]LiveNamespace, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse kubectl namespaces: %w", err)
	}

	out := make([]LiveNamespace, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, LiveNamespace{Name: item.Metadata.Name, Labels: item.Metadata.Labels})
	}

	return out, nil
}

// Compare lists every difference between a cluster's catalog and its live
// namespaces: rows with no namespace, namespaces with no row, and, where both
// exist, every managed label (the managed predicate) that differs. Sorted by
// namespace, then kind, then label.
func Compare(c *Catalog, live []LiveNamespace, managed func(key string) bool) []Mismatch {
	var out []Mismatch

	byName := make(map[string]LiveNamespace, len(live))
	for _, ns := range live {
		byName[ns.Name] = ns
	}

	for i := range c.Namespaces {
		row := &c.Namespaces[i]

		ns, ok := byName[row.Name]
		if !ok {
			out = append(out, Mismatch{Cluster: c.Cluster, Namespace: row.Name, Kind: MissingLive})
			continue
		}

		out = append(out, compareLabels(c.Cluster, row, ns, managed)...)
	}

	for _, ns := range live {
		if c.Row(ns.Name) == nil {
			out = append(out, Mismatch{Cluster: c.Cluster, Namespace: ns.Name, Kind: NotInCatalog})
		}
	}

	slices.SortFunc(out, func(a, b Mismatch) int {
		return strings.Compare(a.Namespace+"\x00"+a.Kind+"\x00"+a.Label, b.Namespace+"\x00"+b.Kind+"\x00"+b.Label)
	})

	return out
}

func compareLabels(cluster string, row *Row, ns LiveNamespace, managed func(string) bool) []Mismatch {
	var out []Mismatch

	keys := map[string]bool{}
	for k := range row.Labels {
		keys[k] = true
	}

	for k := range ns.Labels {
		if managed(k) {
			keys[k] = true
		}
	}

	for _, k := range slices.Sorted(maps.Keys(keys)) {
		want, inRow := row.Labels[k]
		got, inLive := ns.Labels[k]

		m := Mismatch{Cluster: cluster, Namespace: row.Name, Label: k, Want: want, Got: got}

		switch {
		case inRow && !inLive:
			m.Kind = LabelMissing
		case !inRow && inLive:
			m.Kind = LabelExtra
		case want != got:
			m.Kind = LabelDiffers
		default:
			continue
		}

		out = append(out, m)
	}

	return out
}

// ParseKubectlIAMRoleSelectors reads
// `kubectl get iamroleselectors.services.k8s.aws -o json`.
func ParseKubectlIAMRoleSelectors(data []byte) ([]IAMRoleSelector, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				NamespaceSelector struct {
					Names []string `json:"names"`
				} `json:"namespaceSelector"`
			} `json:"spec"`
		} `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse kubectl iamroleselectors: %w", err)
	}

	out := make([]IAMRoleSelector, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, IAMRoleSelector{Name: item.Metadata.Name, Namespaces: item.Spec.NamespaceSelector.Names})
	}

	return out, nil
}

// CompareACKSelectors checks the identity.ackCeiling column against the live
// IAMRoleSelectors: every row with a ceiling is named by one, and no selector
// names a namespace without one.
func CompareACKSelectors(c *Catalog, selectors []IAMRoleSelector) []Mismatch {
	var out []Mismatch

	bound := map[string]string{}

	for _, sel := range selectors {
		for _, ns := range sel.Namespaces {
			bound[ns] = sel.Name

			if r := c.Row(ns); r == nil || !r.Identity.ACKCeiling {
				out = append(out, Mismatch{Cluster: c.Cluster, Namespace: ns, Kind: ACKSelectorUnexpected, Got: sel.Name})
			}
		}
	}

	for i := range c.Namespaces {
		r := &c.Namespaces[i]
		if _, ok := bound[r.Name]; r.Identity.ACKCeiling && !ok {
			out = append(out, Mismatch{Cluster: c.Cluster, Namespace: r.Name, Kind: ACKSelectorMissing})
		}
	}

	slices.SortFunc(out, func(a, b Mismatch) int {
		return strings.Compare(a.Namespace+"\x00"+a.Kind, b.Namespace+"\x00"+b.Kind)
	})

	return out
}
