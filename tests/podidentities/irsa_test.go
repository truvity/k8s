// Package podidentities_test reads the committed golden renders of the
// guardrails-projects podIdentities and checks what each mode puts in a
// cluster: a ServiceAccount with the role annotation and no association in
// IRSA mode, the association and a bare ServiceAccount in Pod Identity mode.
// The goldens are what `just test` keeps equal to what helm renders.
package podidentities_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const defaultKey = "eks.amazonaws.com/role-arn"

type object struct {
	Kind     string
	Metadata struct {
		Name        string
		Namespace   string
		Annotations map[string]string
	}
	Spec struct {
		ClusterName    string `yaml:"clusterName"`
		Namespace      string
		ServiceAccount string `yaml:"serviceAccount"`
		RoleARN        string `yaml:"roleARN"`
	}
}

func render(t *testing.T, golden string) (objs []object, raw string) {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("..", "golden", "guardrails-projects", golden+".yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, doc := range strings.Split(string(b), "\n---\n") {
		var o object
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("%s: %v", golden, err)
		}

		objs = append(objs, o)
	}

	return objs, string(b)
}

func of(objs []object, kind string) (out []object) {
	for _, o := range objs {
		if o.Kind == kind {
			out = append(out, o)
		}
	}

	return out
}

// serviceAccounts maps "namespace/name" to its annotations, for the ServiceAccounts
// of the identities (not the namespaces' default ones, which the chart does not render).
func serviceAccounts(objs []object) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, o := range of(objs, "ServiceAccount") {
		out[o.Metadata.Namespace+"/"+o.Metadata.Name] = o.Metadata.Annotations
	}

	return out
}

func keys(m map[string]map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func TestIRSAModeRendersAnnotatedServiceAccountsOnly(t *testing.T) {
	objs, _ := render(t, "pod-identities-irsa")

	if n := len(of(objs, "PodIdentityAssociation")); n != 0 {
		t.Errorf("IRSA mode renders %d PodIdentityAssociations, want none", n)
	}

	sas := serviceAccounts(objs)
	if got, want := strings.Join(keys(sas), " "), "docs/reader docs/test-s3 shop/test-s3"; got != want {
		t.Fatalf("ServiceAccounts %q, want %q (an empty sweep proves nothing)", got, want)
	}

	for sa, want := range map[string]string{
		"shop/test-s3": "arn:example:iam::placeholder:role/example-test-s3",
		"docs/test-s3": "arn:example:iam::placeholder:role/example-test-s3",
		"docs/reader":  "arn:example:iam::placeholder:role/example-reader",
	} {
		if got := sas[sa][defaultKey]; got != want {
			t.Errorf("%s: %s = %q, want %q", sa, defaultKey, got, want)
		}

		if len(sas[sa]) != 1 {
			t.Errorf("%s: annotations %v, want only the role", sa, sas[sa])
		}
	}
}

func TestEachEntryHasItsOwnMode(t *testing.T) {
	objs, raw := render(t, "pod-identities-mixed")

	// Pod Identity entries (the cluster default and an explicit one): an
	// association each, in its own namespace, and a ServiceAccount without a
	// role annotation.
	var assoc []string
	for _, o := range of(objs, "PodIdentityAssociation") {
		assoc = append(assoc, o.Metadata.Namespace+"/"+o.Metadata.Name+"->"+o.Spec.Namespace+"/"+o.Spec.ServiceAccount)
	}

	sort.Strings(assoc)

	if got, want := strings.Join(assoc, " "), "identity-system/shop-test-s3->shop/test-s3 wide-system/docs-wide->docs/wide"; got != want {
		t.Errorf("associations %q, want %q", got, want)
	}

	sas := serviceAccounts(objs)
	if got, want := strings.Join(keys(sas), " "), "docs/legacy docs/wide shop/legacy shop/test-s3"; got != want {
		t.Fatalf("ServiceAccounts %q, want %q", got, want)
	}

	for _, sa := range []string{"shop/test-s3", "docs/wide"} {
		if len(sas[sa]) != 0 {
			t.Errorf("%s is a Pod Identity entry and must carry no annotation, has %v", sa, sas[sa])
		}
	}

	// The IRSA entry carries the configured key, and only that one.
	for _, sa := range []string{"shop/legacy", "docs/legacy"} {
		if got := sas[sa]["example.com/role-arn"]; got != "arn:example:iam::placeholder:role/example-legacy" {
			t.Errorf("%s: annotation %q", sa, got)
		}

		if _, ok := sas[sa][defaultKey]; ok || len(sas[sa]) != 1 {
			t.Errorf("%s: annotations %v, want only example.com/role-arn", sa, sas[sa])
		}
	}

	// An IRSA entry has no association namespace: the association guard
	// exempts the namespaces of the Pod Identity ones and nothing else.
	if strings.Contains(raw, "null") {
		t.Error("the render holds a null (an IRSA entry has no association namespace to exempt)")
	}

	if !strings.Contains(raw, `!(object.metadata.namespace in [\"identity-system\",\"wide-system\"])`) {
		t.Error("the association guard does not exempt exactly the Pod Identity association namespaces")
	}
}

// The default mode is what it was: an association and a bare ServiceAccount.
func TestPodIdentityIsTheDefault(t *testing.T) {
	objs, _ := render(t, "full")

	if n := len(of(objs, "PodIdentityAssociation")); n != 1 {
		t.Fatalf("%d PodIdentityAssociations, want 1", n)
	}

	a := of(objs, "PodIdentityAssociation")[0]
	if a.Metadata.Name != "ci-shop-test-s3" || a.Metadata.Namespace != "identity-system" || a.Spec.ClusterName != "example" {
		t.Errorf("association %+v", a)
	}

	if sas := serviceAccounts(objs); len(sas["ci-shop/test-s3"]) != 0 {
		t.Errorf("a Pod Identity ServiceAccount carries annotations %v", sas["ci-shop/test-s3"])
	}
}
