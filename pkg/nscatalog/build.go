package nscatalog

import (
	"fmt"
	"maps"
	"slices"

	"github.com/truvity/k8s/pkg/cluster"
)

// Tenant tiers: the value of LabelKeys.Tier, and the tenancy profile (quota)
// of a CI or employee namespace.
const (
	TierPrimary  = "primary"
	TierCI       = "ci"
	TierEmployee = "employee"
)

// The tenancy naming: an employee's sandbox, the shared CI namespace, a CI
// repository's namespace.
const (
	EmployeeNamespacePrefix = "emp-"
	CINamespace             = "ci"
	ciRepoNamespacePrefix   = "ci-"
)

// NATSAccount is Data.NATS for a namespace the shared broker has an account of.
const NATSAccount = "account"

// The role spine.
const (
	bindingDeployer       = "role-spine-deployer"
	bindingDeployerCRDs   = "role-spine-deployer-workload-crds"
	bindingViewer         = "role-spine-viewer"
	bindingTenantOwner    = "tenant-owner"
	clusterRoleEdit       = "edit"
	clusterRoleView       = "view"
	clusterRoleAdmin      = "admin"
	subjectGroup          = "Group"
	subjectUser           = "User"
	labelTrue             = "true"
	nameLabel             = "kubernetes.io/metadata.name"
	selectorOperatorIn    = "In"
	employeeGroupPrefix   = "emp:"
	tenantProjectCI       = "ci"
	tenantProjectEmployee = "employee"
)

// SystemNamespaces are the namespaces every Kubernetes cluster creates.
var SystemNamespaces = []string{"default", "kube-node-lease", "kube-public", "kube-system"}

type (
	// LabelKeys are the label keys the catalog writes. The consumer owns its
	// label taxonomy; an empty key is not written.
	LabelKeys struct {
		// Environment, Project and Application are a namespace's identity.
		Environment string
		Project     string
		Application string
		// Tier is the tenant tier (TierPrimary, TierCI, TierEmployee).
		Tier string
		// WorkloadIdentity admits the namespace to the workload identity
		// trust bundle; its value is "true".
		WorkloadIdentity string
		// TailnetRouter marks a tailnet router's namespace; "true".
		TailnetRouter string
	}

	// Inputs are one cluster's facts.
	Inputs struct {
		// Cluster names the cluster; the role spine's groups are
		// `<cluster>:<namespace>:deployer` and `...:viewer`.
		Cluster string
		// Environment is the identity Environment label of the cluster's
		// project and tenant namespaces.
		Environment string
		Keys        LabelKeys
		// DefaultOwner is the company of a namespace Owners does not name.
		DefaultOwner string
		// Owners are, per company, the namespaces its projects own.
		Owners map[string][]string
		// PodSecurity is the cluster's Pod Security table; nil for none.
		PodSecurity *cluster.PodSecurity
		// Platform are the namespaces the cluster's foundation renders.
		Platform []PlatformNamespace
		// Tenancy is the cluster's tenant inventory; nil for none.
		Tenancy *Tenancy
		// Products are the business projects with a namespace of their name
		// on this cluster.
		Products []Product
		// Listeners are the listener groups that admit routes from namespaces
		// by selector; a namespace a selector names is in the group.
		Listeners []Listener
		// NATS is the shared broker; nil where the cluster runs none.
		NATS *NATS
		// AppProjects are the delivery projects whose destination is a
		// namespace of this cluster.
		AppProjects []AppProject
		// Kargo is the management cluster's Kargo; nil elsewhere.
		Kargo *Kargo
	}

	// PlatformNamespace is one namespace the cluster's foundation renders.
	PlatformNamespace struct {
		Name string
		// Labels are the labels the foundation stamps beside the Pod
		// Security ones.
		Labels map[string]string
		// WorkloadIdentity: the namespace's workloads mint workload
		// identities.
		WorkloadIdentity bool
		// TailnetRouter: the namespace runs a tailnet router.
		TailnetRouter bool
	}

	// Tenancy is a cluster's tenant inventory.
	Tenancy struct {
		// Employees get one sandbox namespace each (EmployeeNamespace).
		Employees []Employee
		// CI is the shared CI namespace (CINamespace).
		CI bool
		// CIRepos get one namespace each (CIRepoNamespace).
		CIRepos []CIRepo
		// PodIdentities are the shared test identities every employee and
		// CI repository namespace gets a ServiceAccount of; empty for none.
		PodIdentities []string
	}

	// Employee is one person with a sandbox namespace.
	Employee struct {
		Slug string
		// Emails are every address the person is known by: User subjects
		// of the owner binding beside the `emp:<slug>` group.
		Emails []string
	}

	// CIRepo is one repository with a CI namespace.
	CIRepo struct {
		Org, Repo string
		// JobUser is the Kubernetes user the repository's jobs sign in as.
		JobUser string
	}

	// Product is one business project's namespace (its name).
	Product struct {
		Name        string
		Application string
		// WorkloadIdentity: the project mints workload identities on some
		// cluster it runs on.
		WorkloadIdentity bool
		// ACKCeiling: the project declares cloud capabilities here, so its
		// ACK role must be bound to its namespace.
		ACKCeiling bool
	}

	// Listener is one listener group and the namespaces it admits routes from.
	Listener struct {
		Name     string
		Selector LabelSelector
	}

	// LabelSelector is a Kubernetes label selector.
	LabelSelector struct {
		MatchLabels      map[string]string
		MatchExpressions []LabelExpression
	}

	// LabelExpression is one matchExpressions entry.
	LabelExpression struct {
		Key      string
		Operator string
		Values   []string
	}

	// NATS is the shared broker: every tenant namespace has an account, and
	// so does each of Accounts.
	NATS struct {
		Accounts []string
	}

	// AppProject is one delivery project and its destination namespace.
	AppProject struct {
		Name      string
		Namespace string
	}

	// Kargo is the management cluster's Kargo: the Project namespaces (by
	// name, to their business project or "" for none), the controller's own
	// namespaces, and the namespaces a component of its renders.
	Kargo struct {
		Projects             map[string]string
		ControllerNamespaces []string
		ComponentNamespaces  []string
	}

	// builder accumulates one cluster's rows.
	builder struct {
		in     *Inputs
		rows   map[string]*Row
		owners map[string]string
	}
)

// Identity is a namespace's identity labels: its environment, project and
// application, under the keys that are set.
func (k LabelKeys) Identity(environment, project, application string) map[string]string {
	out := map[string]string{}

	for key, value := range map[string]string{k.Environment: environment, k.Project: project, k.Application: application} {
		if key != "" {
			out[key] = value
		}
	}

	return out
}

// EmployeeNamespace is an employee's sandbox namespace.
func EmployeeNamespace(slug string) string { return EmployeeNamespacePrefix + slug }

// CIRepoNamespace is a CI repository's namespace.
func CIRepoNamespace(org, repo string) string { return ciRepoNamespacePrefix + org + "-" + repo }

// EmployeeNamespaces are the sandbox namespaces of the inventory, in order.
func (t *Tenancy) EmployeeNamespaces() []string {
	if t == nil {
		return nil
	}

	out := make([]string, 0, len(t.Employees))
	for _, e := range t.Employees {
		out = append(out, EmployeeNamespace(e.Slug))
	}

	return out
}

// Namespaces are every tenant namespace of the inventory: the shared CI
// namespace, one per CI repository, one per employee, in that order.
func (t *Tenancy) Namespaces() []string {
	if t == nil {
		return nil
	}

	var out []string

	if t.CI {
		out = append(out, CINamespace)
	}

	for _, repo := range t.CIRepos {
		out = append(out, CIRepoNamespace(repo.Org, repo.Repo))
	}

	return append(out, t.EmployeeNamespaces()...)
}

// Build derives one cluster's catalog from its facts. Every project, CI and
// employee namespace is written by the guardrails-projects owner, which also
// stamps its Pod Security labels and renders its ACK role selector; the
// foundation writes the platform namespaces; the distribution the system ones;
// Kargo its Project namespaces; any other namespace is a component's own.
func Build(in *Inputs) (*Catalog, error) {
	b := &builder{in: in, rows: map[string]*Row{}, owners: map[string]string{}}

	for company, namespaces := range in.Owners {
		for _, ns := range namespaces {
			b.owners[ns] = company
		}
	}

	for _, ns := range SystemNamespaces {
		r := b.row(ns)
		r.Kind = KindSystem
		r.Guardrails.Writer = WriterSystem
	}

	b.addPodSecurity()
	b.addPlatform()
	b.addTenants()
	b.addProducts()
	b.addListeners()
	b.addData()
	b.addKargo()

	if err := b.addAppProjects(); err != nil {
		return nil, err
	}

	b.finish()

	catalog := &Catalog{Cluster: in.Cluster}
	for _, name := range slices.Sorted(maps.Keys(b.rows)) {
		catalog.Namespaces = append(catalog.Namespaces, *b.rows[name])
	}

	return catalog, nil
}

func (b *builder) row(name string) *Row {
	if r, ok := b.rows[name]; ok {
		return r
	}

	owner := b.in.DefaultOwner
	if company, ok := b.owners[name]; ok {
		owner = company
	}

	r := &Row{
		Name:          name,
		Kind:          KindPlatform,
		Guardrails:    Guardrails{Writer: WriterComponent},
		Observability: Observability{Owner: owner},
	}
	b.rows[name] = r

	return r
}

// label sets key=value on the row; an empty key is not written.
func (b *builder) label(r *Row, key, value string) {
	if key == "" {
		return
	}

	if r.Labels == nil {
		r.Labels = map[string]string{}
	}

	r.Labels[key] = value
}

func (b *builder) labels(r *Row, labels map[string]string) {
	for k, v := range labels {
		b.label(r, k, v)
	}
}

func (b *builder) identity(r *Row, project, application string) {
	b.labels(r, b.in.Keys.Identity(b.in.Environment, project, application))
}

// addPodSecurity: one row per table row, with the labels its owner stamps.
func (b *builder) addPodSecurity() {
	ps := b.in.PodSecurity
	if ps == nil {
		return
	}

	for _, ns := range slices.Sorted(maps.Keys(ps.Namespaces)) {
		entry := ps.Namespaces[ns]
		r := b.row(ns)

		labelWriter := LabelWriterOwner
		if ps.IsBaselineOwned(ns) {
			labelWriter = LabelWriterClusterBaseline
		}

		r.Guardrails.PodSecurity = &PodSecurity{
			Level:       ps.RowLevel(entry),
			Version:     ps.Version,
			Modes:       ps.RowModes(entry),
			Reason:      entry.Reason,
			LabelWriter: labelWriter,
		}
		r.Guardrails.Deletable = entry.Deletable

		b.labels(r, ps.OwnerOf(entry).Labels)
	}
}

// addPlatform: the foundation's namespaces and the labels it stamps.
func (b *builder) addPlatform() {
	for _, p := range b.in.Platform {
		r := b.row(p.Name)
		r.Guardrails.Writer = WriterFoundation

		b.labels(r, p.Labels)

		if p.TailnetRouter {
			b.label(r, b.in.Keys.TailnetRouter, labelTrue)
		}

		if p.WorkloadIdentity {
			b.label(r, b.in.Keys.WorkloadIdentity, labelTrue)
			r.Identity.WorkloadIdentity = true
		}
	}
}

// tenant marks a tenant namespace: its kind, tier, quota, baseline and
// identity labels.
func (b *builder) tenant(ns, kind, tier, quota, project, application string) *Row {
	r := b.row(ns)
	r.Kind = kind
	r.Guardrails.Writer = WriterGuardrailsProjects
	r.Guardrails.TenantBaseline = true
	r.Guardrails.Quota = quota

	b.label(r, b.in.Keys.Tier, tier)
	b.identity(r, project, application)

	return r
}

// addTenants: the employee and CI namespaces, with their role spines and
// test identities.
func (b *builder) addTenants() {
	t := b.in.Tenancy
	if t == nil {
		return
	}

	for _, e := range t.Employees {
		r := b.tenant(EmployeeNamespace(e.Slug), KindEmployee, TierEmployee, TierEmployee, tenantProjectEmployee, e.Slug)

		owner := RoleBinding{Name: bindingTenantOwner, ClusterRole: clusterRoleAdmin,
			Subjects: []Subject{{Kind: subjectGroup, Name: employeeGroupPrefix + e.Slug}}}
		for _, email := range e.Emails {
			owner.Subjects = append(owner.Subjects, Subject{Kind: subjectUser, Name: email})
		}

		r.Guardrails.RoleBindings = []RoleBinding{owner}
		r.Identity.PodIdentities = slices.Clone(t.PodIdentities)
	}

	if t.CI {
		b.tenant(CINamespace, KindCI, TierCI, TierCI, tenantProjectCI, tenantProjectCI)
	}

	for _, repo := range t.CIRepos {
		ns := CIRepoNamespace(repo.Org, repo.Repo)
		r := b.tenant(ns, KindCI, TierCI, TierCI, tenantProjectCI, repo.Repo)

		job := []Subject{{Kind: subjectUser, Name: repo.JobUser}}
		r.Guardrails.RoleBindings = []RoleBinding{
			{Name: bindingDeployer, ClusterRole: clusterRoleEdit, Subjects: job},
			{Name: bindingDeployerCRDs, ClusterRole: WorkloadCRDsClusterRole, Subjects: job},
			b.viewerBinding(ns),
		}
		r.Identity.PodIdentities = slices.Clone(t.PodIdentities)
	}
}

// WorkloadCRDsClusterRole is the ClusterRole of a tenant's rights on operator
// CRDs (aggregated into admin; bound to a CI job's user).
const WorkloadCRDsClusterRole = "tenancy-crd-admin"

// addProducts: every business project's namespace, with its role spine, its
// identity labels and its ACK ceiling.
func (b *builder) addProducts() {
	for _, p := range b.in.Products {
		r := b.tenant(p.Name, KindProduct, TierPrimary, "", p.Name, p.Application)
		r.Project = p.Name
		r.Guardrails.RoleBindings = []RoleBinding{
			{Name: bindingDeployer, ClusterRole: clusterRoleEdit,
				Subjects: []Subject{{Kind: subjectGroup, Name: b.in.Cluster + ":" + p.Name + ":deployer"}}},
			b.viewerBinding(p.Name),
		}

		if p.WorkloadIdentity {
			b.label(r, b.in.Keys.WorkloadIdentity, labelTrue)
		}

		if p.ACKCeiling {
			r.Identity.ACKCeiling = true
			r.Identity.ACKSelectorWriter = WriterGuardrailsProjects
		}
	}
}

func (b *builder) viewerBinding(ns string) RoleBinding {
	return RoleBinding{Name: bindingViewer, ClusterRole: clusterRoleView,
		Subjects: []Subject{{Kind: subjectGroup, Name: b.in.Cluster + ":" + ns + ":viewer"}}}
}

// addListeners: the listener groups that admit a namespace's routes by name.
func (b *builder) addListeners() {
	for _, l := range b.in.Listeners {
		for _, ns := range l.Selector.Names() {
			r := b.row(ns)
			if !slices.Contains(r.Exposure.Listeners, l.Name) {
				r.Exposure.Listeners = append(r.Exposure.Listeners, l.Name)
				slices.Sort(r.Exposure.Listeners)
			}
		}
	}
}

// Names lists the namespaces the selector admits by name: a plain
// kubernetes.io/metadata.name match or an In expression over it. A selector on
// any other label admits no namespace by name.
func (s *LabelSelector) Names() []string {
	var out []string

	if ns, ok := s.MatchLabels[nameLabel]; ok {
		out = append(out, ns)
	}

	for _, expr := range s.MatchExpressions {
		if expr.Key == nameLabel && expr.Operator == selectorOperatorIn {
			out = append(out, expr.Values...)
		}
	}

	return out
}

// addData: every tenant and product namespace can run a CloudNativePG
// cluster (its metrics NetworkPolicy); with a shared broker every tenant
// namespace and each named account has a NATS account.
func (b *builder) addData() {
	tenants := b.in.Tenancy.Namespaces()

	for _, ns := range tenants {
		b.row(ns).Data.CNPGMetrics = true
	}

	for _, p := range b.in.Products {
		b.row(p.Name).Data.CNPGMetrics = true
	}

	if b.in.NATS == nil {
		return
	}

	for _, ns := range append(tenants, b.in.NATS.Accounts...) {
		b.row(ns).Data.NATS = NATSAccount
	}
}

// addKargo: the management cluster's Kargo namespaces.
func (b *builder) addKargo() {
	k := b.in.Kargo
	if k == nil {
		return
	}

	for ns, project := range k.Projects {
		r := b.row(ns)
		r.Kind = KindKargoProject
		r.Guardrails.Writer = WriterKargo
		r.Project = project
	}

	for _, ns := range k.ControllerNamespaces {
		b.row(ns).Guardrails.Writer = WriterKargo
	}

	for _, ns := range k.ComponentNamespaces {
		b.row(ns).Guardrails.Writer = WriterComponent
	}
}

// addAppProjects: the delivery project whose destination is a namespace.
func (b *builder) addAppProjects() error {
	for _, ap := range b.in.AppProjects {
		r := b.row(ap.Namespace)
		if r.Delivery.AppProject != "" {
			return fmt.Errorf("cluster %s: namespace %s is the destination of two AppProjects, %s and %s",
				b.in.Cluster, ap.Namespace, r.Delivery.AppProject, ap.Name)
		}

		r.Delivery.AppProject = ap.Name
	}

	return nil
}

// finish: the guardrails-projects owner stamps the Pod Security labels of its
// namespaces itself; a namespace carrying the workload identity label is in
// the identity concern.
func (b *builder) finish() {
	for _, r := range b.rows {
		if r.Guardrails.Writer == WriterGuardrailsProjects && r.Guardrails.PodSecurity != nil {
			r.Guardrails.PodSecurity.LabelWriter = LabelWriterOwner
		}

		if key := b.in.Keys.WorkloadIdentity; key != "" && r.Labels[key] == labelTrue {
			r.Identity.WorkloadIdentity = true
		}
	}
}
