package cluster_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/cluster"
)

func table() *cluster.PodSecurity {
	return &cluster.PodSecurity{
		Level: "restricted", Version: "v1.34", Modes: []string{"warn", "enforce", "audit"},
		Namespaces: map[string]cluster.PodSecurityNamespace{
			"agents": {Level: "privileged", Reason: "host networking"},
			"apps":   {},
			"canary": {Modes: []string{"warn"}},
		},
		BaselineOwned: []string{"canary"},
	}
}

func TestPodSecurityValidate(t *testing.T) {
	if err := table().Validate("ps"); err != nil {
		t.Fatalf("valid table refused: %v", err)
	}

	row := func(name string, r cluster.PodSecurityNamespace) func(*cluster.PodSecurity) {
		return func(p *cluster.PodSecurity) { p.Namespaces[name] = r }
	}

	for name, tc := range map[string]struct {
		mutate func(*cluster.PodSecurity)
		want   string
	}{
		"level":        {func(p *cluster.PodSecurity) { p.Level = "strict" }, `ps.level "strict" is not one of`},
		"version":      {func(p *cluster.PodSecurity) { p.Version = "" }, "ps.version is required"},
		"modes":        {func(p *cluster.PodSecurity) { p.Modes = nil }, "ps.modes is required"},
		"mode":         {func(p *cluster.PodSecurity) { p.Modes = []string{"deny"} }, `ps.modes: "deny" is not one of`},
		"row mode":     {row("x", cluster.PodSecurityNamespace{Modes: []string{"x"}}), `ps.namespaces.x: modes: "x"`},
		"row level":    {row("x", cluster.PodSecurityNamespace{Level: "open", Reason: "r"}), `ps.namespaces.x: level "open"`},
		"no reason":    {row("x", cluster.PodSecurityNamespace{Level: "baseline"}), "ps.namespaces.x departs from the default level"},
		"baseline row": {func(p *cluster.PodSecurity) { p.BaselineOwned = []string{"ghost"} }, `ps.baseline_owned: "ghost"`},
	} {
		p := table()
		tc.mutate(p)

		if err := p.Validate("ps"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", name, err, tc.want)
		}
	}

	// The default level needs no reason.
	p := table()
	p.Namespaces["x"] = cluster.PodSecurityNamespace{Level: "restricted"}

	if err := p.Validate("ps"); err != nil {
		t.Errorf("a row at the default level refused: %v", err)
	}
}

func TestPodSecurityResolve(t *testing.T) {
	r := table().Resolve()

	if !reflect.DeepEqual(r.Modes, []string{"audit", "enforce", "warn"}) {
		t.Errorf("modes %v, want sorted", r.Modes)
	}

	if _, ok := r.Owner["canary"]; ok {
		t.Error("a baseline-owned row has an owner entry")
	}

	agents := r.Owner["agents"]
	if agents.Labels["pod-security.kubernetes.io/enforce"] != "privileged" ||
		agents.Labels["pod-security.kubernetes.io/enforce-version"] != "v1.34" || len(agents.Labels) != 6 {
		t.Errorf("agents labels %v", agents.Labels)
	}

	want := map[string]string{
		cluster.SyncOptionsAnnotation:       cluster.KeepSyncOptions,
		cluster.PodSecurityReasonAnnotation: "host networking",
	}
	if !reflect.DeepEqual(agents.Annotations, want) {
		t.Errorf("agents annotations %v, want %v", agents.Annotations, want)
	}

	var none *cluster.PodSecurity
	if none.Resolve() != nil {
		t.Error("a nil table resolves to something")
	}
}

func TestPodSecurityDerivedRows(t *testing.T) {
	p := table()

	if p.WithDeletableRows([]string{"emp-a"}, nil) != p || p.WithDeletableRows(nil, []string{"warn"}) != p {
		t.Error("no modes or no namespaces must return the table itself")
	}

	q := p.WithDeletableRows([]string{"emp-a"}, []string{"warn"})
	if _, ok := p.Namespaces["emp-a"]; ok {
		t.Fatal("the input table was mutated")
	}

	owner := q.Resolve().Owner["emp-a"]
	if len(owner.Annotations) != 0 || !reflect.DeepEqual(owner.Labels, map[string]string{
		"pod-security.kubernetes.io/warn": "restricted", "pod-security.kubernetes.io/warn-version": "v1.34",
	}) {
		t.Errorf("emp-a owner %+v", owner)
	}

	r := p.Resolve()
	on := r.WithOwnedRow("extra", cluster.PodSecurityNamespace{})

	if _, ok := r.Owner["extra"]; ok {
		t.Error("WithOwnedRow mutated its input")
	}

	if _, ok := on.Owner["extra"]; !ok {
		t.Error("WithOwnedRow added no owner entry")
	}

	var none *cluster.ResolvedPodSecurity
	if none.WithOwnedRow("x", cluster.PodSecurityNamespace{}) != nil {
		t.Error("a nil table gains a row")
	}
}

func TestDeriveLabelGuard(t *testing.T) {
	in := cluster.LabelGuardInputs{
		ProtectedDomains:       []string{"example.io"},
		ControllerUser:         "system:serviceaccount:gitops:controller",
		DeployerUserPrefix:     "deployer/",
		BreakGlassUserPrefixes: []string{"admin/"},
		AdminGroups:            []string{"dev:admin"},
	}

	workload := cluster.DeriveLabelGuard(in)
	if !reflect.DeepEqual(workload.AllowedUserPrefixes, []string{"deployer/", "admin/"}) || workload.AllowedUsers != nil ||
		!reflect.DeepEqual(workload.ProtectedPrefixes, []string{cluster.PodSecurityLabelPrefix}) {
		t.Errorf("workload guard %+v", workload)
	}

	in.Management = true

	mgmt := cluster.DeriveLabelGuard(in)
	if !reflect.DeepEqual(mgmt.AllowedUsers, []string{in.ControllerUser}) || !reflect.DeepEqual(mgmt.AllowedUserPrefixes, []string{"admin/"}) {
		t.Errorf("management guard %+v", mgmt)
	}

	if err := mgmt.Validate(); err != nil {
		t.Errorf("valid guard refused: %v", err)
	}

	if err := (&cluster.LabelGuard{ProtectedDomains: []string{"x"}}).Validate(); err == nil {
		t.Error("a guard with no principal accepted")
	}

	if err := (&cluster.LabelGuard{AllowedGroups: []string{"g"}}).Validate(); err == nil {
		t.Error("a guard with no protected label accepted")
	}
}
