package cluster

import (
	"fmt"
	"maps"
	"slices"
)

// Pod Security Admission label and annotation keys. The reason annotation is
// the cluster-baseline chart's own (its `reasonAnnotation` default), so a
// Namespace its owner labels reads the same as one the chart labels.
const (
	PodSecurityLabelPrefix      = "pod-security.kubernetes.io/"
	PodSecurityReasonAnnotation = "cluster-baseline/psa-reason"
	// SyncOptionsAnnotation and KeepSyncOptions are the Argo CD guard an
	// owner stamps on a Namespace it labels: removing the Namespace from one
	// Application's render must never prune it.
	SyncOptionsAnnotation = "argocd.argoproj.io/sync-options"
	KeepSyncOptions       = "Prune=false,Delete=false"
)

var (
	// PodSecurityLevels are the three Pod Security Standards.
	PodSecurityLevels = []string{"privileged", "baseline", "restricted"}
	// PodSecurityModes are the three Pod Security Admission modes.
	PodSecurityModes = []string{"audit", "enforce", "warn"}
)

type (
	// PodSecurity is one cluster's Pod Security Admission table: the default
	// level, version and modes, and one row per namespace. It is the input of
	// the cluster-baseline chart's `podSecurity` (Resolve gives the same labels
	// the chart's `cluster-baseline.resolve` helper renders), for a consumer
	// whose Namespace owners stamp the labels themselves.
	PodSecurity struct {
		// Level is the default level; Version the default version label
		// ("latest", or a pinned minor before anything is enforced).
		Level   string
		Version string
		// Modes are the modes that render a label: warn, audit, enforce.
		Modes []string
		// Namespaces is the table. An empty row takes Level and Modes.
		Namespaces map[string]PodSecurityNamespace
		// BaselineOwned are the rows the cluster-baseline chart labels
		// itself, because no owner of the consumer renders them.
		BaselineOwned []string
	}

	// PodSecurityNamespace is one table row.
	PodSecurityNamespace struct {
		// Level replaces the default level; a level other than the default
		// needs a Reason, so the exemption can be reviewed and retired.
		Level  string
		Reason string
		// Modes narrows the table's modes for this row (a warn-first
		// rollout: warn and audit now, enforce after the warn period).
		Modes []string
		// Deletable omits the keep guard the owner otherwise stamps: the
		// Namespace goes when its row does.
		Deletable bool
	}

	// PodSecurityOwner is what a Namespace's owner stamps for one row: the
	// labels and the annotations.
	PodSecurityOwner struct {
		Labels      map[string]string
		Annotations map[string]string
	}

	// ResolvedPodSecurity is a table with its modes and BaselineOwned sorted
	// and, per owner-rendered row (the table minus BaselineOwned), what the
	// owner stamps.
	ResolvedPodSecurity struct {
		PodSecurity

		Owner map[string]PodSecurityOwner
	}
)

// Validate refuses a table the cluster-baseline chart would refuse: a level or
// mode outside the standards, no version or no modes, a row that departs from
// the default level without a reason, and a BaselineOwned name with no row.
// The error names the offending key under prefix (the consumer's spelling of
// the table, e.g. `cluster "devel": pod_security`).
func (p *PodSecurity) Validate(prefix string) error {
	if !slices.Contains(PodSecurityLevels, p.Level) {
		return fmt.Errorf("%s.level %q is not one of %v", prefix, p.Level, PodSecurityLevels)
	}

	if p.Version == "" {
		return fmt.Errorf("%s.version is required (\"latest\" or a pinned minor)", prefix)
	}

	if len(p.Modes) == 0 {
		return fmt.Errorf("%s.modes is required", prefix)
	}

	if err := ValidatePodSecurityModes(prefix+".modes", p.Modes); err != nil {
		return err
	}

	for _, ns := range slices.Sorted(maps.Keys(p.Namespaces)) {
		row := p.Namespaces[ns]
		if err := ValidatePodSecurityModes(fmt.Sprintf("%s.namespaces.%s: modes", prefix, ns), row.Modes); err != nil {
			return err
		}

		if row.Level != "" && !slices.Contains(PodSecurityLevels, row.Level) {
			return fmt.Errorf("%s.namespaces.%s: level %q is not one of %v", prefix, ns, row.Level, PodSecurityLevels)
		}

		if row.Level != "" && row.Level != p.Level && row.Reason == "" {
			return fmt.Errorf("%s.namespaces.%s departs from the default level and has no reason", prefix, ns)
		}
	}

	for _, ns := range p.BaselineOwned {
		if _, ok := p.Namespaces[ns]; !ok {
			return fmt.Errorf("%s.baseline_owned: %q is not in the table's namespaces", prefix, ns)
		}
	}

	return nil
}

// ValidatePodSecurityModes refuses a mode outside PodSecurityModes; what names
// the list in the error.
func ValidatePodSecurityModes(what string, modes []string) error {
	for _, m := range modes {
		if !slices.Contains(PodSecurityModes, m) {
			return fmt.Errorf("%s: %q is not one of %v", what, m, PodSecurityModes)
		}
	}

	return nil
}

// WithDeletableRows returns the table plus one deletable row per namespace,
// labelled with just modes (a warn-first rollout of namespaces that come and
// go, such as per-person sandboxes). No modes or no namespaces return p
// itself; p is never mutated.
func (p *PodSecurity) WithDeletableRows(namespaces, modes []string) *PodSecurity {
	if p == nil || len(modes) == 0 || len(namespaces) == 0 {
		return p
	}

	out := *p
	out.Namespaces = make(map[string]PodSecurityNamespace, len(p.Namespaces)+len(namespaces))
	maps.Copy(out.Namespaces, p.Namespaces)

	for _, ns := range namespaces {
		out.Namespaces[ns] = PodSecurityNamespace{Modes: modes, Deletable: true}
	}

	return &out
}

// IsBaselineOwned reports whether the cluster-baseline chart labels ns itself.
func (p *PodSecurity) IsBaselineOwned(ns string) bool {
	return slices.Contains(p.BaselineOwned, ns)
}

// RowLevel is the level of a row: its own, or the default.
func (p *PodSecurity) RowLevel(row PodSecurityNamespace) string {
	if row.Level != "" {
		return row.Level
	}

	return p.Level
}

// RowModes is the sorted modes of a row: its own, or the default.
func (p *PodSecurity) RowModes(row PodSecurityNamespace) []string {
	if len(row.Modes) > 0 {
		return slices.Sorted(slices.Values(row.Modes))
	}

	return slices.Sorted(slices.Values(p.Modes))
}

// OwnerOf is what a Namespace's owner stamps for a row: `<mode>: <level>` and
// `<mode>-version: <version>` per mode, the keep guard unless the row is
// deletable, and the reason annotation where there is a reason. The same keys
// and values the cluster-baseline chart renders for the row.
func (p *PodSecurity) OwnerOf(row PodSecurityNamespace) PodSecurityOwner {
	level := p.RowLevel(row)

	owner := PodSecurityOwner{
		Labels:      map[string]string{},
		Annotations: map[string]string{SyncOptionsAnnotation: KeepSyncOptions},
	}
	if row.Deletable {
		owner.Annotations = map[string]string{}
	}

	for _, mode := range p.RowModes(row) {
		owner.Labels[PodSecurityLabelPrefix+mode] = level
		owner.Labels[PodSecurityLabelPrefix+mode+"-version"] = p.Version
	}

	if row.Reason != "" {
		owner.Annotations[PodSecurityReasonAnnotation] = row.Reason
	}

	return owner
}

// Resolve sorts the table's modes and BaselineOwned and computes what the
// owner stamps on every row the chart does not label itself. Nil is nil.
func (p *PodSecurity) Resolve() *ResolvedPodSecurity {
	if p == nil {
		return nil
	}

	out := &ResolvedPodSecurity{
		PodSecurity: PodSecurity{
			Level:         p.Level,
			Version:       p.Version,
			Modes:         slices.Sorted(slices.Values(p.Modes)),
			Namespaces:    p.Namespaces,
			BaselineOwned: slices.Sorted(slices.Values(p.BaselineOwned)),
		},
		Owner: map[string]PodSecurityOwner{},
	}

	for ns, row := range p.Namespaces {
		if p.IsBaselineOwned(ns) {
			continue
		}

		out.Owner[ns] = out.OwnerOf(row)
	}

	return out
}

// WithOwnedRow returns the resolved table plus one owner-rendered row (a
// namespace that exists only while some switch is on). r is never mutated.
func (r *ResolvedPodSecurity) WithOwnedRow(ns string, row PodSecurityNamespace) *ResolvedPodSecurity {
	if r == nil {
		return nil
	}

	out := *r
	out.Namespaces = make(map[string]PodSecurityNamespace, len(r.Namespaces)+1)
	maps.Copy(out.Namespaces, r.Namespaces)
	out.Namespaces[ns] = row
	out.Owner = make(map[string]PodSecurityOwner, len(r.Owner)+1)
	maps.Copy(out.Owner, r.Owner)
	out.Owner[ns] = out.OwnerOf(row)

	return &out
}
