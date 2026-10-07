package nscatalog_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/cluster"
	"github.com/truvity/k8s/pkg/nscatalog"
)

var keys = nscatalog.LabelKeys{
	Environment:      "platform.example.io/environment",
	Project:          "platform.example.io/project",
	Application:      "platform.example.io/application",
	Tier:             "tenancy.example.io/tier",
	WorkloadIdentity: "identity.example.io/workload-identity",
	TailnetRouter:    "example.io/tailnet-router",
}

func buildInputs() *nscatalog.Inputs {
	return &nscatalog.Inputs{
		Cluster:      "dev",
		Environment:  "dev",
		Keys:         keys,
		DefaultOwner: "acme",
		Owners:       map[string][]string{"partner": {"shop"}},
		PodSecurity: &cluster.PodSecurity{
			Level: "restricted", Version: "v1.34", Modes: []string{"warn", "audit"},
			Namespaces: map[string]cluster.PodSecurityNamespace{
				"node-agents": {Level: "privileged", Reason: "host networking"},
				"shop":        {},
				"kube-system": {Level: "privileged", Reason: "the CNI"},
				"emp-alice":   {Modes: []string{"warn"}, Deletable: true},
			},
		},
		Platform: []nscatalog.PlatformNamespace{
			{Name: "node-agents"},
			{Name: "broker", WorkloadIdentity: true},
			{Name: "router-a", TailnetRouter: true},
			{Name: "gateway", Labels: map[string]string{"gateway.example.io/route-grant-health": "true"}},
		},
		Tenancy: &nscatalog.Tenancy{
			Employees:     []nscatalog.Employee{{Slug: "alice", Emails: []string{"alice@example.com"}}},
			CI:            true,
			CIRepos:       []nscatalog.CIRepo{{Org: "org", Repo: "app", JobUser: "ci:org/app"}},
			PodIdentities: []string{"test-s3"},
		},
		Products: []nscatalog.Product{
			{Name: "shop", Application: "storefront", WorkloadIdentity: true, ACKCeiling: true},
		},
		Listeners: []nscatalog.Listener{
			{Name: "shared", Selector: nscatalog.LabelSelector{MatchExpressions: []nscatalog.LabelExpression{
				{Key: "kubernetes.io/metadata.name", Operator: "In", Values: []string{"emp-alice", "shop"}},
			}}},
			{Name: "health", Selector: nscatalog.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "gateway"}}},
			{Name: "by-label", Selector: nscatalog.LabelSelector{MatchLabels: map[string]string{"team": "x"}}},
		},
		NATS:        &nscatalog.NATS{Accounts: []string{"shop"}},
		AppProjects: []nscatalog.AppProject{{Name: "shop-dev", Namespace: "shop"}},
	}
}

func mustBuild(t *testing.T, in *nscatalog.Inputs) *nscatalog.Catalog {
	t.Helper()

	c, err := nscatalog.Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	return c
}

func mustRow(t *testing.T, c *nscatalog.Catalog, name string) *nscatalog.Row {
	t.Helper()

	r := c.Row(name)
	if r == nil {
		t.Fatalf("no row %s in %v", name, c.Names())
	}

	return r
}

func TestBuildRows(t *testing.T) {
	c := mustBuild(t, buildInputs())

	want := []string{
		"broker", "ci", "ci-org-app", "default", "emp-alice", "gateway", "kube-node-lease", "kube-public",
		"kube-system", "node-agents", "router-a", "shop",
	}
	if got := c.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows %v, want %v", got, want)
	}

	if r := mustRow(t, c, "default"); r.Kind != nscatalog.KindSystem || r.Guardrails.Writer != nscatalog.WriterSystem {
		t.Errorf("default: %s/%s", r.Kind, r.Guardrails.Writer)
	}
}

func TestBuildProduct(t *testing.T) {
	r := mustRow(t, mustBuild(t, buildInputs()), "shop")

	if r.Kind != nscatalog.KindProduct || r.Project != "shop" || r.Guardrails.Writer != nscatalog.WriterGuardrailsProjects {
		t.Errorf("shop: kind %s project %s writer %s", r.Kind, r.Project, r.Guardrails.Writer)
	}

	// Its owner (guardrails-projects) stamps the labels.
	if ps := r.Guardrails.PodSecurity; ps == nil || ps.LabelWriter != nscatalog.LabelWriterOwner {
		t.Errorf("shop: pod security %+v, want the owner as label writer", ps)
	}

	wantLabels := map[string]string{
		keys.Environment: "dev", keys.Project: "shop", keys.Application: "storefront",
		keys.Tier: nscatalog.TierPrimary, keys.WorkloadIdentity: "true",
		"pod-security.kubernetes.io/audit": "restricted", "pod-security.kubernetes.io/audit-version": "v1.34",
		"pod-security.kubernetes.io/warn": "restricted", "pod-security.kubernetes.io/warn-version": "v1.34",
	}
	if !reflect.DeepEqual(r.Labels, wantLabels) {
		t.Errorf("shop labels %v, want %v", r.Labels, wantLabels)
	}

	if !r.Identity.WorkloadIdentity || !r.Identity.ACKCeiling || r.Identity.ACKSelectorWriter != nscatalog.WriterGuardrailsProjects {
		t.Errorf("shop identity %+v", r.Identity)
	}

	if !r.Data.CNPGMetrics || r.Data.NATS != nscatalog.NATSAccount {
		t.Errorf("shop data %+v", r.Data)
	}

	if r.Observability.Owner != "partner" || r.Delivery.AppProject != "shop-dev" {
		t.Errorf("shop owner %s, app project %s", r.Observability.Owner, r.Delivery.AppProject)
	}

	if len(r.Guardrails.RoleBindings) != 2 || r.Guardrails.RoleBindings[0].Subjects[0].Name != "dev:shop:deployer" ||
		r.Guardrails.RoleBindings[1].Subjects[0].Name != "dev:shop:viewer" {
		t.Errorf("shop role spine %+v", r.Guardrails.RoleBindings)
	}

	if !reflect.DeepEqual(r.Exposure.Listeners, []string{"shared"}) || r.Exposure.RouteGrantBusiness {
		t.Errorf("shop exposure %+v", r.Exposure)
	}
}

func TestBuildTenants(t *testing.T) {
	c := mustBuild(t, buildInputs())

	emp := mustRow(t, c, "emp-alice")
	if emp.Kind != nscatalog.KindEmployee || emp.Guardrails.Quota != nscatalog.TierEmployee || !emp.Guardrails.Deletable {
		t.Errorf("emp-alice: %+v", emp.Guardrails)
	}

	owner := emp.Guardrails.RoleBindings[0]
	if owner.ClusterRole != "admin" || owner.Subjects[0].Name != "emp:alice" || owner.Subjects[1].Name != "alice@example.com" {
		t.Errorf("emp-alice owner %+v", owner)
	}

	if emp.Labels[keys.Project] != "employee" || emp.Labels[keys.Application] != "alice" ||
		emp.Labels["pod-security.kubernetes.io/warn"] != "restricted" || emp.Labels["pod-security.kubernetes.io/audit"] != "" {
		t.Errorf("emp-alice labels %v", emp.Labels)
	}

	repo := mustRow(t, c, "ci-org-app")
	if repo.Kind != nscatalog.KindCI || repo.Labels[keys.Application] != "app" ||
		!reflect.DeepEqual(repo.Identity.PodIdentities, []string{"test-s3"}) {
		t.Errorf("ci-org-app: %+v", repo)
	}

	if b := repo.Guardrails.RoleBindings; len(b) != 3 || b[1].ClusterRole != nscatalog.WorkloadCRDsClusterRole || b[0].Subjects[0].Name != "ci:org/app" {
		t.Errorf("ci-org-app role spine %+v", b)
	}

	shared := mustRow(t, c, "ci")
	if shared.Identity.PodIdentities != nil || len(shared.Guardrails.RoleBindings) != 0 || shared.Data.NATS != nscatalog.NATSAccount {
		t.Errorf("ci: %+v", shared)
	}
}

func TestBuildPlatform(t *testing.T) {
	c := mustBuild(t, buildInputs())

	if r := mustRow(t, c, "router-a"); r.Labels[keys.TailnetRouter] != "true" || r.Guardrails.Writer != nscatalog.WriterFoundation {
		t.Errorf("router-a: %+v", r)
	}

	if r := mustRow(t, c, "broker"); !r.Identity.WorkloadIdentity || r.Labels[keys.WorkloadIdentity] != "true" {
		t.Errorf("broker: %+v", r)
	}

	if r := mustRow(t, c, "gateway"); !reflect.DeepEqual(r.Exposure.Listeners, []string{"health"}) ||
		r.Labels["gateway.example.io/route-grant-health"] != "true" {
		t.Errorf("gateway: %+v", r)
	}

	ks := mustRow(t, c, "kube-system")
	if ks.Guardrails.PodSecurity.LabelWriter != nscatalog.LabelWriterClusterBaseline || ks.Guardrails.PodSecurity.Level != "privileged" {
		t.Errorf("kube-system: %+v", ks.Guardrails.PodSecurity)
	}

	if r := mustRow(t, c, "node-agents"); r.Guardrails.PodSecurity.Reason != "host networking" || r.Observability.Owner != "acme" {
		t.Errorf("node-agents: %+v", r)
	}
}

func TestBuildKargo(t *testing.T) {
	in := buildInputs()
	in.Kargo = &nscatalog.Kargo{
		Projects:             map[string]string{"shop-delivery": "shop", "platform-x": ""},
		ControllerNamespaces: []string{"kargo"},
		ComponentNamespaces:  []string{"kargo-users"},
	}

	c := mustBuild(t, in)

	if r := mustRow(t, c, "shop-delivery"); r.Kind != nscatalog.KindKargoProject || r.Project != "shop" || r.Guardrails.Writer != nscatalog.WriterKargo {
		t.Errorf("shop-delivery: %+v", r)
	}

	if r := mustRow(t, c, "kargo"); r.Guardrails.Writer != nscatalog.WriterKargo {
		t.Errorf("kargo: %+v", r)
	}

	if r := mustRow(t, c, "kargo-users"); r.Guardrails.Writer != nscatalog.WriterComponent {
		t.Errorf("kargo-users: %+v", r)
	}
}

func TestBuildRefusesTwoAppProjectsForOneNamespace(t *testing.T) {
	in := buildInputs()
	in.AppProjects = append(in.AppProjects, nscatalog.AppProject{Name: "other", Namespace: "shop"})

	if _, err := nscatalog.Build(in); err == nil || !strings.Contains(err.Error(), "two AppProjects") {
		t.Fatalf("err %v, want the two-AppProjects refusal", err)
	}
}

func TestBuildWithoutOptionalFacts(t *testing.T) {
	c := mustBuild(t, &nscatalog.Inputs{Cluster: "bare", DefaultOwner: "acme"})

	if got := c.Names(); !reflect.DeepEqual(got, nscatalog.SystemNamespaces) {
		t.Fatalf("rows %v, want only the system namespaces", got)
	}

	for _, r := range c.Namespaces {
		if r.Labels != nil {
			t.Errorf("%s carries labels %v", r.Name, r.Labels)
		}
	}
}

func TestEmptyLabelKeyIsNotWritten(t *testing.T) {
	in := buildInputs()
	in.Keys.Tier = ""

	if r := mustRow(t, mustBuild(t, in), "shop"); r.Labels[""] != "" || len(r.Labels) != 8 {
		t.Errorf("shop labels %v", r.Labels)
	}
}

func TestTenancyNamespaces(t *testing.T) {
	tn := &nscatalog.Tenancy{
		CI:        true,
		CIRepos:   []nscatalog.CIRepo{{Org: "o", Repo: "r"}},
		Employees: []nscatalog.Employee{{Slug: "bob"}},
	}

	if got, want := tn.Namespaces(), []string{"ci", "ci-o-r", "emp-bob"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Namespaces %v, want %v", got, want)
	}

	var none *nscatalog.Tenancy
	if none.Namespaces() != nil || none.EmployeeNamespaces() != nil {
		t.Error("a nil inventory has namespaces")
	}
}

func TestLabelKeysIdentity(t *testing.T) {
	got := nscatalog.LabelKeys{Environment: "e", Project: "p"}.Identity("dev", "shop", "front")
	if want := map[string]string{"e": "dev", "p": "shop"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Identity %v, want %v", got, want)
	}
}

func TestBuildPodSecurityDefaultsAndExceptions(t *testing.T) {
	in := buildInputs()
	in.Components = []nscatalog.Component{
		{Name: "rollouts", LabelWriter: nscatalog.LabelWriterClusterBaseline},
		{Name: "own"},
	}
	in.Kargo = &nscatalog.Kargo{
		Projects:             map[string]string{"platform-x": ""},
		ControllerNamespaces: []string{"kargo-system"},
		ComponentNamespaces:  []string{"kargo-users"},
	}

	c := mustBuild(t, in)

	// A row no exception names takes the defaults; Kargo's project namespace
	// and a component the cluster-baseline chart labels are labelled by it.
	for ns, writer := range map[string]string{
		"broker": nscatalog.LabelWriterOwner, "platform-x": nscatalog.LabelWriterClusterBaseline,
		"rollouts": nscatalog.LabelWriterClusterBaseline, "own": nscatalog.LabelWriterOwner,
		"kube-system": nscatalog.LabelWriterClusterBaseline,
	} {
		ps := mustRow(t, c, ns).Guardrails.PodSecurity
		if ps == nil || ps.LabelWriter != writer {
			t.Errorf("%s: pod security %+v, want label writer %s", ns, ps, writer)
		}
	}

	// Not labelled: the system namespaces without an exception and Kargo's own.
	for _, ns := range []string{"default", "kube-public", "kube-node-lease", "kargo-system", "kargo-users"} {
		if r := mustRow(t, c, ns); r.Guardrails.PodSecurity != nil || len(r.Labels) != 0 {
			t.Errorf("%s: labelled %+v", ns, r)
		}
	}
}

func TestBuildPodSecurityExceptionWithoutRow(t *testing.T) {
	in := buildInputs()
	in.PodSecurity.Namespaces["ghost"] = cluster.PodSecurityNamespace{Level: "baseline", Reason: "r"}

	if _, err := nscatalog.Build(in); err == nil || !strings.Contains(err.Error(), `exception "ghost"`) {
		t.Errorf("err %v, want an exception with no row refused", err)
	}
}
