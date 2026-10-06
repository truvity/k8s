package cluster

import "errors"

type (
	// LabelGuard is the cluster-baseline chart's `labelGuard` principals and
	// protected labels: only the named principals may set, change or remove a
	// protected namespace label. The yaml keys are the chart's.
	LabelGuard struct {
		ProtectedPrefixes   []string `yaml:"protectedPrefixes,omitempty"`
		ProtectedDomains    []string `yaml:"protectedDomains,omitempty"`
		AllowedUsers        []string `yaml:"allowedUsers,omitempty"`
		AllowedUserPrefixes []string `yaml:"allowedUserPrefixes,omitempty"`
		AllowedGroups       []string `yaml:"allowedGroups,omitempty"`
	}

	// LabelGuardInputs are the facts a cluster's label guard is derived from.
	LabelGuardInputs struct {
		// ProtectedDomains are the label domains the platform owns (a key
		// whose prefix is one of them, or a subdomain of one, is protected).
		// The Pod Security labels are always protected.
		ProtectedDomains []string
		// Management: the cluster runs its own GitOps controller, which
		// writes its namespaces as ControllerUser.
		Management     bool
		ControllerUser string
		// DeployerUserPrefix is, on a cluster the management cluster
		// deploys to, the prefix of the username the management cluster's
		// deployer signs in as. Ignored on the management cluster.
		DeployerUserPrefix string
		// BreakGlassUserPrefixes are the cluster's own administrators that
		// sign in by username (an infrastructure-as-code pipeline, a
		// break-glass role).
		BreakGlassUserPrefixes []string
		// AdminGroups are the cluster's admin groups.
		AdminGroups []string
	}
)

// DeriveLabelGuard names who may write a cluster's protected namespace labels:
// its GitOps writer (the controller on the management cluster, the management
// cluster's deployer elsewhere), the break-glass principals and the admin
// groups. The Pod Security labels are always protected.
func DeriveLabelGuard(in LabelGuardInputs) LabelGuard {
	g := LabelGuard{
		ProtectedPrefixes:   []string{PodSecurityLabelPrefix},
		ProtectedDomains:    append([]string(nil), in.ProtectedDomains...),
		AllowedUserPrefixes: append([]string(nil), in.BreakGlassUserPrefixes...),
		AllowedGroups:       append([]string(nil), in.AdminGroups...),
	}

	switch {
	case in.Management && in.ControllerUser != "":
		g.AllowedUsers = []string{in.ControllerUser}
	case !in.Management && in.DeployerUserPrefix != "":
		g.AllowedUserPrefixes = append([]string{in.DeployerUserPrefix}, g.AllowedUserPrefixes...)
	}

	return g
}

// Validate refuses what the chart refuses: a guard with no allowed principal
// (nobody could label a namespace) or no protected label.
func (g *LabelGuard) Validate() error {
	if len(g.AllowedUsers) == 0 && len(g.AllowedUserPrefixes) == 0 && len(g.AllowedGroups) == 0 {
		return errors.New("labelGuard: name at least one allowed principal (allowedUsers, allowedUserPrefixes or allowedGroups)")
	}

	if len(g.ProtectedPrefixes) == 0 && len(g.ProtectedDomains) == 0 {
		return errors.New("labelGuard: name the protected labels (protectedPrefixes or protectedDomains)")
	}

	return nil
}
