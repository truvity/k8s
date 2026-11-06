package admission_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/cel-go/common/types/ref"
	"go.yaml.in/yaml/v3"
)

// The image allow-list enforces per row: a row with images.enforce goes into a
// second binding that denies, the main binding observes the others. These
// tests read the committed goldens and check the wiring a cluster would see:
// which binding selects which namespace, and that the two partition the
// checked namespaces exactly.

const nsLabel = "kubernetes.io/metadata.name"

type bindingDoc struct {
	Kind     string
	Metadata struct{ Name string }
	Spec     struct {
		PolicyName        string   `yaml:"policyName"`
		ValidationActions []string `yaml:"validationActions"`
		MatchResources    *struct {
			NamespaceSelector struct {
				MatchLabels      map[string]string `yaml:"matchLabels"`
				MatchExpressions []struct {
					Key      string
					Operator string
					Values   []string
				} `yaml:"matchExpressions"`
			} `yaml:"namespaceSelector"`
		} `yaml:"matchResources"`
	}
}

// selects reports whether the binding's namespaceSelector admits a namespace,
// by the one label the chart uses. An expression it does not know fails the
// test, so a new kind of selector is not silently read as "everything".
func (b bindingDoc) selects(t *testing.T, ns string) bool {
	t.Helper()

	if b.Spec.MatchResources == nil {
		return true
	}

	sel := b.Spec.MatchResources.NamespaceSelector
	if len(sel.MatchLabels) != 0 {
		t.Fatalf("binding %s selects by matchLabels; the chart uses matchExpressions on %s", b.Metadata.Name, nsLabel)
	}

	for _, e := range sel.MatchExpressions {
		if e.Key != nsLabel {
			t.Fatalf("binding %s selects by label %q, want %s", b.Metadata.Name, e.Key, nsLabel)
		}

		in := slices.Contains(e.Values, ns)

		switch e.Operator {
		case "In":
			if !in {
				return false
			}
		case "NotIn":
			if in {
				return false
			}
		default:
			t.Fatalf("binding %s: operator %q", b.Metadata.Name, e.Operator)
		}
	}

	return true
}

// expressionValues returns the values of the binding's one expression with the
// operator, or nil when it has none.
func (b bindingDoc) expressionValues(operator string) []string {
	if b.Spec.MatchResources == nil {
		return nil
	}

	for _, e := range b.Spec.MatchResources.NamespaceSelector.MatchExpressions {
		if e.Operator == operator {
			return e.Values
		}
	}

	return nil
}

// loadAll returns every policy and binding of a golden render.
func loadAll(t *testing.T, golden string) (pols []policy, bindings []bindingDoc) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "golden", "guardrails-projects", golden+".yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var p policy
		if err := yaml.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatalf("%s: %v", golden, err)
		}

		switch p.Kind {
		case "ValidatingAdmissionPolicy":
			pols = append(pols, p)
		case "ValidatingAdmissionPolicyBinding":
			var b bindingDoc
			if err := yaml.Unmarshal([]byte(doc), &b); err != nil {
				t.Fatalf("%s: %v", golden, err)
			}

			bindings = append(bindings, b)
		}
	}

	return pols, bindings
}

// checkedNamespaces is the key set of the policy's allow map.
func checkedNamespaces(t *testing.T, pol policy) []string {
	t.Helper()

	raw, ok := eval(t, env(t), variableExpr(t, pol, "allow"), map[string]any{}).(map[ref.Val]ref.Val)
	if !ok {
		t.Fatal("the allow variable is not a map")
	}

	var out []string
	for k := range raw {
		out = append(out, fmt.Sprint(k.Value()))
	}

	slices.Sort(out)

	return out
}

func TestImageAllowListEnforcesPerRow(t *testing.T) {
	pols, bindings := loadAll(t, "image-allow-list-enforce")

	if len(pols) != 1 || len(bindings) != 2 {
		t.Fatalf("want one policy and two bindings, got %d and %d", len(pols), len(bindings))
	}

	pol := pols[0]
	name := pol.Metadata.Name

	var observe, enforce bindingDoc

	for _, b := range bindings {
		if b.Spec.PolicyName != name {
			t.Errorf("binding %s names policy %q, want %q", b.Metadata.Name, b.Spec.PolicyName, name)
		}

		switch b.Metadata.Name {
		case name:
			observe = b
		case name + "-enforce":
			enforce = b
		default:
			t.Errorf("unexpected binding %q", b.Metadata.Name)
		}
	}

	if got := strings.Join(observe.Spec.ValidationActions, ","); got != "Warn,Audit" {
		t.Errorf("the main binding observes, has %q", got)
	}

	if got := strings.Join(enforce.Spec.ValidationActions, ","); got != "Deny,Audit" {
		t.Errorf("the -enforce binding denies, has %q", got)
	}

	// The two selectors are each other's complement, over the one label.
	wantEnforced := []string{"docs", "shop"}
	if got := enforce.expressionValues("In"); !slices.Equal(got, wantEnforced) {
		t.Errorf("the -enforce binding selects In %v, want %v", got, wantEnforced)
	}

	if got := observe.expressionValues("NotIn"); !slices.Equal(got, wantEnforced) {
		t.Errorf("the main binding selects NotIn %v, want %v", got, wantEnforced)
	}

	// The partition: every checked namespace is in exactly one binding.
	checked := checkedNamespaces(t, pol)
	if want := []string{"ci-shop", "docs", "shop", "tools"}; !slices.Equal(checked, want) {
		t.Fatalf("the golden checks %v, want %v (an empty sweep proves nothing)", checked, want)
	}

	for _, ns := range checked {
		o, e := observe.selects(t, ns), enforce.selects(t, ns)
		if o == e {
			t.Errorf("namespace %s: observed=%v enforced=%v, want exactly one", ns, o, e)
		}

		if want := slices.Contains(wantEnforced, ns); e != want {
			t.Errorf("namespace %s: enforced=%v, want %v", ns, e, want)
		}
	}

	// A namespace the policy does not check is never denied: it reaches only the
	// main binding, and the policy skips it.
	for _, ns := range []string{"sandbox", "legacy", "kube-system"} {
		if enforce.selects(t, ns) {
			t.Errorf("the -enforce binding selects the unchecked namespace %s", ns)
		}
	}

	// The policy is the one every namespace shares: a refusal in an enforced
	// row names the image, exactly as in an observed one.
	for _, ns := range []string{"shop", "ci-shop"} {
		p := pod{ns: ns, containers: []string{"evil.test/x"}}
		got := admitPod(t, pol, "CREATE", "", p.object(), map[string]any{})
		if !strings.Contains(got, "image evil.test/x is not on the allow-list of namespace "+ns) {
			t.Errorf("%s: got %q", ns, got)
		}
	}
}

func TestImageAllowListWithoutEnforcedRowsHasOneBinding(t *testing.T) {
	for _, tc := range []struct {
		golden  string
		actions string
	}{
		{"image-allow-list", "Warn,Audit"},
		{"image-allow-list-deny", "Deny,Audit"},
		// Every row already denies: a row's enforce adds no second binding.
		{"image-allow-list-deny-enforce-row", "Deny,Audit"},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			pols, bindings := loadAll(t, tc.golden)
			if len(pols) != 1 || len(bindings) != 1 {
				t.Fatalf("want one policy and one binding, got %d and %d", len(pols), len(bindings))
			}

			b := bindings[0]
			if b.Spec.MatchResources != nil {
				t.Errorf("the only binding selects namespaces; it must select every one")
			}

			if got := strings.Join(b.Spec.ValidationActions, ","); got != tc.actions {
				t.Errorf("actions %q, want %q", got, tc.actions)
			}

			if len(checkedNamespaces(t, pols[0])) == 0 {
				t.Error("the golden checks no namespace")
			}
		})
	}
}
