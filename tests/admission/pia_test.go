// Package admission evaluates the ValidatingAdmissionPolicies the charts render
// with a CEL environment, so an expression that does not compile, or that
// admits what it should refuse, fails here and not on a cluster. It reads the
// committed golden render (tests/golden), which `just test` keeps equal to what
// helm renders, so no helm and no cluster are needed.
//
// The environment is cel-go with the strings extension: the same function set
// the API server's CEL environment offers for the functions the policies use.
package admission_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"go.yaml.in/yaml/v3"
)

type policy struct {
	Kind     string
	Metadata struct{ Name string }
	Spec     struct {
		FailurePolicy     string                              `yaml:"failurePolicy"`
		PolicyName        string                              `yaml:"policyName"`
		ValidationActions []string                            `yaml:"validationActions"`
		MatchConditions   []struct{ Name, Expression string } `yaml:"matchConditions"`
		Variables         []struct{ Name, Expression string }
		Validations       []struct{ Expression, Message, Reason string }
	}
}

// load returns the ValidatingAdmissionPolicy and its binding of a golden render.
func load(t *testing.T, golden string) (pol, binding policy) {
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
			pol = p
		case "ValidatingAdmissionPolicyBinding":
			binding = p
		}
	}

	if pol.Kind == "" || binding.Kind == "" {
		t.Fatalf("%s renders no policy and binding", golden)
	}

	return pol, binding
}

func env(t *testing.T) *cel.Env {
	t.Helper()

	e, err := cel.NewEnv(
		ext.Strings(),
		cel.Variable("object", cel.DynType),
		cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType),
		cel.Variable("variables", cel.DynType),
	)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func eval(t *testing.T, e *cel.Env, expr string, act map[string]any) any {
	t.Helper()

	ast, iss := e.Compile(expr)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("expression does not compile: %v\n%s", iss.Err(), expr)
	}

	prg, err := e.Program(ast)
	if err != nil {
		t.Fatal(err)
	}

	out, _, err := prg.Eval(act)
	if err != nil {
		t.Fatalf("expression fails: %v\n%s", err, expr)
	}

	return out.Value()
}

// admit runs the policy as the API server does: matchConditions (all must be
// true or the policy does not apply), then variables, then validations. It
// returns "" when the object is admitted or skipped, else the first message.
func admit(t *testing.T, pol policy, op string, obj, old map[string]any) (verdict string) {
	t.Helper()

	e := env(t)
	act := map[string]any{
		"object":    obj,
		"oldObject": old,
		"request":   map[string]any{"operation": op},
		"variables": map[string]any{},
	}

	for _, mc := range pol.Spec.MatchConditions {
		if eval(t, e, mc.Expression, act) != true {
			return "skipped: " + mc.Name
		}
	}

	vars := map[string]any{}

	for _, v := range pol.Spec.Variables {
		val := eval(t, e, v.Expression, act)
		// A CEL map comes back as a native Go map of ref.Val; round-trip through
		// the interface so the next expression sees plain data.
		vars[v.Name] = native(val)
		act["variables"] = vars
	}

	for _, v := range pol.Spec.Validations {
		if eval(t, e, v.Expression, act) != true {
			return v.Message
		}
	}

	return ""
}

// native converts what cel returns for a map or list into plain Go values.
func native(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := map[string]any{}
		for k, val := range x {
			out[k.(string)] = native(val)
		}

		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = native(val)
		}

		return out
	default:
		return v
	}
}

func pia(ns, specNs, role string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"namespace": ns},
		"spec":     map[string]any{"namespace": specNs, "roleARN": role},
	}
}

// piaRef is an association that names its role by reference (an ACK Role CR).
// fromNs "" leaves roleRef.from.namespace unset.
func piaRef(ns, specNs, name, fromNs string) map[string]any {
	from := map[string]any{"name": name}
	if fromNs != "" {
		from["namespace"] = fromNs
	}

	return map[string]any{
		"metadata": map[string]any{"namespace": ns},
		"spec":     map[string]any{"namespace": specNs, "roleRef": map[string]any{"from": from}},
	}
}

// withSpec is a shop association with one extra spec field (none when key is "").
func withSpec(key string, val any) map[string]any {
	spec := map[string]any{"namespace": "shop"}
	if key != "" {
		spec[key] = val
	}

	return map[string]any{"metadata": map[string]any{"namespace": "shop"}, "spec": spec}
}

// withRef adds a same-namespace roleRef to an association.
func withRef(obj map[string]any, name string) map[string]any {
	obj["spec"].(map[string]any)["roleRef"] = map[string]any{"from": map[string]any{"name": name}}

	return obj
}

const (
	rolePrefix   = "arn:example:iam::placeholder:role/"
	skipExempt   = "skipped: platform-namespaces-are-exempt"
	skipSpec     = "skipped: only-spec-changes"
	badRole      = "spec.roleARN must be a role"
	badNamespace = "spec.namespace must equal"
	noCeiling    = "no IAM role ceiling"
	badRef       = "spec.roleRef must name a Role"
	noRole       = "names its role"
)

func TestPodIdentityAdmissionPolicy(t *testing.T) {
	oldShop := pia("shop", "shop", rolePrefix+"example-shop-old")
	pol, binding := load(t, "pia-admission")

	for _, tc := range []struct {
		name    string
		op      string
		obj     map[string]any
		old     map[string]any
		verdict string // "" admitted; "skipped:" prefix; else a message fragment
	}{
		{"role of the project", "CREATE", pia("shop", "shop", rolePrefix+"example-shop.vault"), nil, ""},
		{"second prefix of the namespace", "CREATE", pia("docs", "docs", rolePrefix+"example-docs-legacy-static"), nil, ""},
		{"another project's role", "CREATE", pia("shop", "shop", rolePrefix+"example-docs.static"), nil, badRole},
		{"a legacy name the namespace has no prefix for", "CREATE", pia("shop", "shop", rolePrefix+"example-shop-vault"), nil, badRole},
		{"a path in the name", "CREATE", pia("shop", "shop", rolePrefix+"example-shop./x"), nil, badRole},
		{"a path after the name", "CREATE", pia("shop", "shop", rolePrefix+"example-shop.a/b"), nil, badRole},
		{"another account", "CREATE", pia("shop", "shop", "arn:example:iam::other:role/example-shop.vault"), nil, badRole},
		{"a controller's role", "CREATE", pia("shop", "shop", rolePrefix+"example-ack-iam"), nil, badRole},
		{"binding another namespace", "CREATE", pia("shop", "kube-system", rolePrefix+"example-shop.vault"), nil, badNamespace},
		{"a namespace with no prefixes", "CREATE", pia("ci-shop", "ci-shop", rolePrefix+"example-ci.x"), nil, noCeiling},
		{"a namespace with no row", "CREATE", pia("sandbox", "sandbox", rolePrefix+"example-sandbox.x"), nil, noCeiling},
		{"a Role of the project by reference", "CREATE", piaRef("shop", "shop", "example-shop.vault", ""), nil, ""},
		{"a Role of the project by reference, namespace spelled out", "CREATE", piaRef("shop", "shop", "example-shop.vault", "shop"), nil, ""},
		{"a Role by a legacy name of the namespace", "CREATE", piaRef("docs", "docs", "example-docs-legacy-static", ""), nil, ""},
		{"a Role of another namespace", "CREATE", piaRef("shop", "shop", "example-shop.vault", "docs"), nil, badRef},
		{"a Role of another namespace, empty namespace", "CREATE", piaRef("shop", "shop", "example-shop.vault", " "), nil, badRef},
		{"another project's Role by reference", "CREATE", piaRef("shop", "shop", "example-docs.static", ""), nil, badRef},
		{"a controller's Role by reference", "CREATE", piaRef("shop", "shop", "example-ack-iam", ""), nil, badRef},
		{"a legacy name the namespace has no prefix for, by reference", "CREATE", piaRef("shop", "shop", "example-shop-vault", ""), nil, badRef},
		{"a reference with no name", "CREATE", withSpec("roleRef", map[string]any{"from": map[string]any{}}), nil, badRef},
		{"a reference with no from", "CREATE", withSpec("roleRef", map[string]any{}), nil, badRef},
		{"a reference in a namespace with no prefixes", "CREATE", piaRef("ci-shop", "ci-shop", "example-ci.x", ""), nil, noCeiling},
		{"a reference binding another namespace", "CREATE", piaRef("shop", "kube-system", "example-shop.vault", ""), nil, badNamespace},
		{"a good roleARN beside a foreign reference", "CREATE", withRef(pia("shop", "shop", rolePrefix+"example-shop.vault"), "example-docs.x"), nil, badRef},
		{"a foreign roleARN beside a good reference", "CREATE", withRef(pia("shop", "shop", rolePrefix+"example-docs.x"), "example-shop.vault"), nil, badRole},
		{"neither a roleARN nor a reference", "CREATE", withSpec("", nil), nil, noRole},
		{"an update to a bad reference", "UPDATE", piaRef("shop", "shop", "example-docs.x", ""), piaRef("shop", "shop", "example-shop.x", ""), badRef},
		{"an update to a good reference", "UPDATE", piaRef("shop", "shop", "example-shop.y", ""), piaRef("shop", "shop", "example-shop.x", ""), ""},
		{"an exempt namespace may bind another", "CREATE", pia("platform-db", "shop", rolePrefix+"example-pg-shop"), nil, skipExempt},
		{"the chart's association namespace is exempt", "CREATE", pia("identity-system", "ci-shop", rolePrefix+"example-test-s3"), nil, skipExempt},
		{"an update that leaves spec alone", "UPDATE", pia("shop", "shop", rolePrefix+"example-shop-old"), oldShop, skipSpec},
		{"an update to a bad role", "UPDATE", pia("shop", "shop", rolePrefix+"example-docs.x"), pia("shop", "shop", rolePrefix+"example-shop.x"), badRole},
		{"an update to a good role", "UPDATE", pia("shop", "shop", rolePrefix+"example-shop.y"), pia("shop", "shop", rolePrefix+"example-shop-old"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := tc.old
			if old == nil {
				old = map[string]any{}
			}

			got := admit(t, pol, tc.op, tc.obj, old)
			if tc.verdict == "" && got != "" || !strings.Contains(got, tc.verdict) {
				t.Errorf("got %q, want %q", got, tc.verdict)
			}
		})
	}

	if got := strings.Join(binding.Spec.ValidationActions, ","); got != "Warn,Audit" {
		t.Errorf("the binding observes first (Warn, Audit), has %q", got)
	}

	if binding.Spec.PolicyName != pol.Metadata.Name {
		t.Errorf("the binding names policy %q, the policy is %q", binding.Spec.PolicyName, pol.Metadata.Name)
	}

	if pol.Spec.FailurePolicy != "Fail" {
		t.Errorf("failurePolicy %q", pol.Spec.FailurePolicy)
	}
}

func TestPodIdentityAdmissionCanEnforce(t *testing.T) {
	pol, binding := load(t, "pia-admission-deny")

	if got := strings.Join(binding.Spec.ValidationActions, ","); got != "Deny,Audit" {
		t.Errorf("binding actions %q", got)
	}

	if pol.Metadata.Name != "pia-guard" || binding.Metadata.Name != "pia-guard" || binding.Spec.PolicyName != "pia-guard" {
		t.Errorf("the name is a value: policy %q, binding %q -> %q", pol.Metadata.Name, binding.Metadata.Name, binding.Spec.PolicyName)
	}

	if pol.Spec.FailurePolicy != "Ignore" {
		t.Errorf("failurePolicy %q", pol.Spec.FailurePolicy)
	}

	// No exemptNamespaces: a namespace the first case exempted is now evaluated.
	if got := admit(t, pol, "CREATE", pia("platform-db", "shop", rolePrefix+"example-pg-shop"), map[string]any{}); !strings.Contains(got, badNamespace) {
		t.Errorf("platform-db is not exempt here, got %q", got)
	}
}
