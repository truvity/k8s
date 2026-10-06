package nscatalog_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/nscatalog"
)

func fixture() *nscatalog.Catalog {
	return &nscatalog.Catalog{
		Cluster: "devel",
		Namespaces: []nscatalog.Row{
			{
				Name: "url-shortener", Kind: nscatalog.KindProduct, Project: "url-shortener",
				Guardrails: nscatalog.Guardrails{Writer: nscatalog.WriterPulumiTenancy, TenantBaseline: true},
				Identity:   nscatalog.Identity{ACKCeiling: true, ACKSelectorWriter: nscatalog.WriterPulumiTenancy},
				Labels: map[string]string{
					"pod-security.kubernetes.io/enforce": "restricted",
					"platform.example.io/project":        "url-shortener",
				},
			},
			{
				Name: "dms", Kind: nscatalog.KindProduct, Project: "dms",
				Guardrails: nscatalog.Guardrails{Writer: nscatalog.WriterTenants},
				Identity:   nscatalog.Identity{ACKCeiling: true, ACKSelectorWriter: "none"},
			},
			{Name: "default", Kind: nscatalog.KindSystem, Guardrails: nscatalog.Guardrails{Writer: nscatalog.WriterSystem}},
		},
	}
}

func TestMarshalRoundTripsSorted(t *testing.T) {
	t.Parallel()

	data, err := nscatalog.Marshal(fixture(), "# GENERATED header\n")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(string(data), "# GENERATED header") {
		t.Fatalf("no provenance stamp on line 1:\n%s", data)
	}

	back, err := nscatalog.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}

	if got := back.Names(); !slices.Equal(got, []string{"default", "dms", "url-shortener"}) {
		t.Fatalf("rows not sorted or lost: %v", got)
	}

	if back.Row("url-shortener").Labels["platform.example.io/project"] != "url-shortener" {
		t.Fatalf("labels lost in the round trip")
	}

	again, err := nscatalog.Marshal(back, "# GENERATED header\n")
	if err != nil {
		t.Fatal(err)
	}

	if string(again) != string(data) {
		t.Fatalf("Marshal is not stable:\n%s\n---\n%s", data, again)
	}
}

func TestCompareReportsEveryKind(t *testing.T) {
	t.Parallel()

	live := []nscatalog.LiveNamespace{
		{Name: "default"},
		{Name: "url-shortener", Labels: map[string]string{
			"pod-security.kubernetes.io/enforce":             "baseline", // differs
			"gateway.example.io/route-grant-business-origin": "true",     // extra, managed
			"kubernetes.io/metadata.name":                    "url-shortener",
			"argocd.argoproj.io/instance":                    "x", // not managed: ignored
		}},
		{Name: "billing"}, // not in the catalog
	}

	var got []string
	for _, m := range nscatalog.Compare(fixture(), live, nscatalog.ManagedByDomains("example.io", "example.com")) {
		got = append(got, m.Namespace+" "+m.Kind+" "+m.Label)
	}

	want := []string{
		"billing not-in-catalog ",
		"dms missing-live ",
		"url-shortener label-differs pod-security.kubernetes.io/enforce",
		"url-shortener label-extra gateway.example.io/route-grant-business-origin",
		"url-shortener label-missing platform.example.io/project",
	}

	if !slices.Equal(got, want) {
		t.Fatalf("Compare:\n got %q\nwant %q", got, want)
	}
}

func TestCompareACKSelectors(t *testing.T) {
	t.Parallel()

	selectors := []nscatalog.IAMRoleSelector{
		{Name: "ack-project-url-shortener", Namespaces: []string{"url-shortener"}},
		{Name: "ack-project-default", Namespaces: []string{"default"}},
	}

	var got []string
	for _, m := range nscatalog.CompareACKSelectors(fixture(), selectors) {
		got = append(got, m.Namespace+" "+m.Kind)
	}

	want := []string{"default ack-selector-unexpected", "dms ack-selector-missing"}
	if !slices.Equal(got, want) {
		t.Fatalf("CompareACKSelectors:\n got %q\nwant %q", got, want)
	}
}

func TestParseKubectl(t *testing.T) {
	t.Parallel()

	ns, err := nscatalog.ParseKubectlNamespaces([]byte(`{"items":[{"metadata":{"name":"a","labels":{"x":"y"}}}]}`))
	if err != nil || len(ns) != 1 || ns[0].Name != "a" || ns[0].Labels["x"] != "y" {
		t.Fatalf("namespaces: %v %v", ns, err)
	}

	sel, err := nscatalog.ParseKubectlIAMRoleSelectors([]byte(`{"items":[{"metadata":{"name":"s"},"spec":{"namespaceSelector":{"names":["a"]}}}]}`))
	if err != nil || len(sel) != 1 || sel[0].Name != "s" || !slices.Equal(sel[0].Namespaces, []string{"a"}) {
		t.Fatalf("selectors: %v %v", sel, err)
	}
}
