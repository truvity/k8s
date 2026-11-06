package admission_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/cel-go/common/types/ref"
)

// The image allow-list is evaluated here as the API server does: the same
// matchConditions, variables, validations, messageExpression and
// auditAnnotations, over a Pod as a map. The committed golden is the policy
// under test, so a render that drops or loosens a rule fails here.

const (
	notListed = "is not on the allow-list of namespace"
	skipNs    = "skipped: namespace-has-an-allow-list"
	skipImgs  = "skipped: only-image-changes"
	digest    = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// pod is a Pod of a namespace; its containers are the images given, one each.
type pod struct {
	ns         string
	containers []string
	inits      []string
	ephemeral  []string
	volumes    []string // image volumes, by reference
}

func (p pod) object() map[string]any {
	named := func(prefix string, images []string) []any {
		var out []any
		for i, img := range images {
			out = append(out, map[string]any{"name": fmt.Sprintf("%s%d", prefix, i), "image": img})
		}

		return out
	}

	spec := map[string]any{"containers": named("c", p.containers)}
	if p.inits != nil {
		spec["initContainers"] = named("i", p.inits)
	}

	if p.ephemeral != nil {
		spec["ephemeralContainers"] = named("e", p.ephemeral)
	}

	if p.volumes != nil {
		var vols []any
		for i, ref := range p.volumes {
			vols = append(vols, map[string]any{"name": fmt.Sprintf("v%d", i), "image": map[string]any{"reference": ref}})
		}

		spec["volumes"] = vols
	}

	return map[string]any{"metadata": map[string]any{"namespace": p.ns}, "spec": spec}
}

// admitPod runs the policy for a Pod request. It returns "" when the pod is
// admitted, "skipped: <matchCondition>" when the policy does not apply, else
// the denial message (messageExpression) followed by the audit annotation.
func admitPod(t *testing.T, pol policy, op, subResource string, obj, old map[string]any) string {
	t.Helper()

	e := env(t)
	ns, _ := obj["metadata"].(map[string]any)["namespace"].(string)
	act := map[string]any{
		"object":    obj,
		"oldObject": old,
		"request":   map[string]any{"operation": op, "namespace": ns, "subResource": subResource},
		"variables": map[string]any{},
	}

	for _, mc := range pol.Spec.MatchConditions {
		if eval(t, e, mc.Expression, act) != true {
			return "skipped: " + mc.Name
		}
	}

	vars := map[string]any{}

	for _, v := range pol.Spec.Variables {
		vars[v.Name] = native(eval(t, e, v.Expression, act))
		act["variables"] = vars
	}

	for _, v := range pol.Spec.Validations {
		if eval(t, e, v.Expression, act) == true {
			continue
		}

		msg := v.Message
		if v.MessageExpression != "" {
			msg = eval(t, e, v.MessageExpression, act).(string)
		}

		for _, a := range pol.Spec.AuditAnnotations {
			msg += " | " + a.Key + "=" + eval(t, e, a.ValueExpression, act).(string)
		}

		return msg
	}

	return ""
}

func TestImageAllowListPolicy(t *testing.T) {
	pol, binding := load(t, "image-allow-list")

	reg := "registry.example/"
	acme := reg + "acme/"
	shopBase := reg + "shared/base"
	toolbox := "registry.example:5000/debug/toolbox"
	runner := "mirror.test/acme/ci/runner"

	// Admitted, or skipped (verdict is a "skipped: ..." prefix).
	// "shop" lists the set acme (a prefix and an exact repository), the exact
	// repository shared/base, and the common entries (a prefix and an exact
	// repository with a port). "docs" lists acme and globex. "ci-shop" lists a
	// prefix of its own. "legacy" is exempt; "sandbox" has no row.
	admitted := []struct {
		name string
		p    pod
	}{
		{"a path prefix", pod{ns: "shop", containers: []string{acme + "api:1"}}},
		{"a path prefix, no tag", pod{ns: "shop", containers: []string{acme + "api"}}},
		{"a path prefix, digest only", pod{ns: "shop", containers: []string{acme + "api" + digest}}},
		{"a path prefix, tag and digest", pod{ns: "shop", containers: []string{acme + "api:1" + digest}}},
		{"below the prefix", pod{ns: "shop", containers: []string{acme + "team/api:1"}}},
		{"an exact repository with a tag", pod{ns: "shop", containers: []string{shopBase + ":1"}}},
		{"an exact repository, no tag", pod{ns: "shop", containers: []string{shopBase}}},
		{"an exact repository, digest only", pod{ns: "shop", containers: []string{shopBase + digest}}},
		{"an exact repository, tag and digest", pod{ns: "shop", containers: []string{shopBase + ":1" + digest}}},
		{"a common prefix", pod{ns: "shop", containers: []string{reg + "mesh/proxy:3"}}},
		{"a common repository with a port", pod{ns: "shop", containers: []string{toolbox + ":latest"}}},
		{"an exact repository of the set", pod{ns: "shop", containers: []string{runner + ":1"}}},
		{"two sets", pod{ns: "docs", containers: []string{reg + "globex/x:1", acme + "y:2"}}},
		{"a prefix of the row itself", pod{ns: "ci-shop", containers: []string{"mirror.test/acme/ci/other:1"}}},
		{"an init container on the list", pod{ns: "shop", containers: []string{acme + "api:1"}, inits: []string{acme + "init:1"}}},
		{"an ephemeral container on the list", pod{ns: "shop", containers: []string{acme + "api:1"}, ephemeral: []string{toolbox}}},
		{"an image volume on the list", pod{ns: "shop", containers: []string{acme + "api:1"}, volumes: []string{acme + "model:1"}}},
		{"every kind, all on the list", pod{
			ns: "shop", containers: []string{acme + "a:1", acme + "b:1"}, inits: []string{acme + "i:1"},
			ephemeral: []string{toolbox}, volumes: []string{acme + "v:1"},
		}},
	}

	for _, tc := range admitted {
		t.Run("admits "+tc.name, func(t *testing.T) {
			if got := admitPod(t, pol, "CREATE", "", tc.p.object(), map[string]any{}); got != "" {
				t.Errorf("refused: %s", got)
			}
		})
	}

	skipped := []struct {
		name    string
		p       pod
		verdict string
	}{
		{"a namespace with no row", pod{ns: "sandbox", containers: []string{"nginx"}}, skipNs},
		{"an exempt row", pod{ns: "legacy", containers: []string{"nginx"}}, skipNs},
		{"a namespace nothing names", pod{ns: "kube-system", containers: []string{"nginx"}}, skipNs},
	}

	for _, tc := range skipped {
		t.Run("skips "+tc.name, func(t *testing.T) {
			if got := admitPod(t, pol, "CREATE", "", tc.p.object(), map[string]any{}); got != tc.verdict {
				t.Errorf("got %q, want %q", got, tc.verdict)
			}
		})
	}

	// Refused: the message must name the offending image and the namespace.
	refused := []struct {
		name  string
		p     pod
		image string // the first offender the message names
	}{
		{"another company's prefix", pod{ns: "shop", containers: []string{reg + "globex/x:1"}}, reg + "globex/x:1"},
		{"a prefix that only starts like ours", pod{ns: "shop", containers: []string{reg + "acme-evil/x:1"}}, reg + "acme-evil/x:1"},
		{"the repository named as the prefix, without its slash", pod{ns: "shop", containers: []string{reg + "acme"}}, reg + "acme"},
		{"a longer repository than the exact entry", pod{ns: "shop", containers: []string{shopBase + "2:1"}}, shopBase + "2:1"},
		{"a repository beside the exact entry", pod{ns: "shop", containers: []string{shopBase + "-evil"}}, shopBase + "-evil"},
		{"a repository below the exact entry", pod{ns: "shop", containers: []string{shopBase + "/sub:1"}}, shopBase + "/sub:1"},
		{"a longer repository by digest", pod{ns: "shop", containers: []string{shopBase + "x" + digest}}, shopBase + "x" + digest},
		{"an unqualified image", pod{ns: "shop", containers: []string{"nginx"}}, "nginx"},
		{"an unqualified image with a tag", pod{ns: "shop", containers: []string{"nginx:1.27"}}, "nginx:1.27"},
		{"an unqualified path", pod{ns: "shop", containers: []string{"library/nginx"}}, "library/nginx"},
		{"a host that only starts like ours", pod{ns: "shop", containers: []string{"registry.example.evil.test/acme/x"}}, "registry.example.evil.test/acme/x"},
		{"a host with another port", pod{ns: "shop", containers: []string{"registry.example:5001/debug/toolbox"}}, "registry.example:5001/debug/toolbox"},
		{"another row's prefix", pod{ns: "docs", containers: []string{"mirror.test/acme/ci/other:1"}}, "mirror.test/acme/ci/other:1"},
		{"the other way round", pod{ns: "ci-shop", containers: []string{reg + "globex/x:1"}}, reg + "globex/x:1"},
		{"an init container off the list", pod{ns: "shop", containers: []string{acme + "a:1"}, inits: []string{"evil.test/i"}}, "evil.test/i"},
		{"an ephemeral container off the list", pod{ns: "shop", containers: []string{acme + "a:1"}, ephemeral: []string{"evil.test/dbg"}}, "evil.test/dbg"},
		{"an image volume off the list", pod{ns: "shop", containers: []string{acme + "a:1"}, volumes: []string{"evil.test/v"}}, "evil.test/v"},
		{"one good, one bad", pod{ns: "shop", containers: []string{acme + "a:1", "evil.test/x"}}, "evil.test/x"},
		{"the bad one first", pod{ns: "shop", containers: []string{"evil.test/x", acme + "a:1"}}, "evil.test/x"},
		{"upper case in the host", pod{ns: "shop", containers: []string{"REGISTRY.example/acme/x"}}, "REGISTRY.example/acme/x"},
		{"upper case in the path", pod{ns: "shop", containers: []string{reg + "ACME/x"}}, reg + "ACME/x"},
		{"a parent step", pod{ns: "shop", containers: []string{acme + "../evil/x"}}, acme + "../evil/x"},
		{"a current-directory step", pod{ns: "shop", containers: []string{acme + "./evil"}}, acme + "./evil"},
		{"a double slash after the host", pod{ns: "shop", containers: []string{"registry.example//acme/x"}}, "registry.example//acme/x"},
		{"a double slash in the path", pod{ns: "shop", containers: []string{acme + "/x"}}, acme + "/x"},
		{"a leading slash", pod{ns: "shop", containers: []string{"/" + acme + "x"}}, "/" + acme + "x"},
		{"an empty image", pod{ns: "shop", containers: []string{""}}, ""},
		{"a scheme", pod{ns: "shop", containers: []string{"https://" + acme + "x"}}, "https://" + acme + "x"},
	}

	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			got := admitPod(t, pol, "CREATE", "", tc.p.object(), map[string]any{})
			want := "image " + tc.image + " " + strings.SplitN(notListed, " ", 2)[0] // "image <x> is"
			if !strings.Contains(got, want) || !strings.Contains(got, notListed+" "+tc.p.ns) {
				t.Errorf("got %q, want a refusal naming %q in %q", got, tc.image, tc.p.ns)
			}
		})
	}

	// The audit annotation names every offender, not only the first one the
	// message shows: a Warn on a pod a controller creates is seen by nobody.
	t.Run("the audit annotation lists every offender", func(t *testing.T) {
		p := pod{
			ns: "shop", containers: []string{"evil.test/a", acme + "ok:1"}, inits: []string{"evil.test/b"},
			ephemeral: []string{"evil.test/c"}, volumes: []string{"evil.test/d"},
		}

		got := admitPod(t, pol, "CREATE", "", p.object(), map[string]any{})
		if !strings.Contains(got, "(and 3 more)") || !strings.Contains(got, "offending-images=evil.test/a, evil.test/b, evil.test/c, evil.test/d") {
			t.Errorf("got %q", got)
		}
	})

	// UPDATE: only a changed image is judged, so a label edit on a pod that
	// predates the list, or a finalizer removal, is never refused.
	t.Run("update", func(t *testing.T) {
		bad := pod{ns: "shop", containers: []string{"evil.test/x"}}
		good := pod{ns: "shop", containers: []string{acme + "a:1"}}
		extra := pod{ns: "shop", containers: []string{acme + "a:1"}, ephemeral: []string{"evil.test/dbg"}}

		if got := admitPod(t, pol, "UPDATE", "", bad.object(), bad.object()); got != skipImgs {
			t.Errorf("an update that leaves the images alone: got %q", got)
		}

		if got := admitPod(t, pol, "UPDATE", "", good.object(), bad.object()); got != "" {
			t.Errorf("an update to an image on the list: got %q", got)
		}

		if got := admitPod(t, pol, "UPDATE", "", bad.object(), good.object()); !strings.Contains(got, "image evil.test/x is") {
			t.Errorf("an update to an image off the list: got %q", got)
		}

		if got := admitPod(t, pol, "UPDATE", "ephemeralcontainers", extra.object(), good.object()); !strings.Contains(got, "image evil.test/dbg is") {
			t.Errorf("an ephemeral container added off the list: got %q", got)
		}

		if got := admitPod(t, pol, "UPDATE", "", good.object(), good.object()); got != skipImgs {
			t.Errorf("got %q", got)
		}
	})

	// The binding observes first and names the policy.
	if got := strings.Join(binding.Spec.ValidationActions, ","); got != "Warn,Audit" {
		t.Errorf("the binding observes first (Warn, Audit), has %q", got)
	}

	if binding.Spec.PolicyName != pol.Metadata.Name || pol.Metadata.Name != "pod-image-allow-list" {
		t.Errorf("binding -> %q, policy %q", binding.Spec.PolicyName, pol.Metadata.Name)
	}

	if pol.Spec.FailurePolicy != "Fail" {
		t.Errorf("failurePolicy %q", pol.Spec.FailurePolicy)
	}

	// An empty sweep must not pass as green: the golden has a real policy over
	// real namespaces, and this table kept its breadth.
	e := env(t)
	allowRaw, ok := eval(t, e, variableExpr(t, pol, "allow"), map[string]any{}).(map[ref.Val]ref.Val)
	if !ok {
		t.Fatal("the allow variable is not a map")
	}

	allow := map[string]bool{}
	for k := range allowRaw {
		allow[fmt.Sprint(k.Value())] = true
	}

	if len(allow) < 3 {
		t.Errorf("the golden's allow map holds %d namespaces, the table needs the rows it names", len(allow))
	}

	for _, ns := range []string{"shop", "docs", "ci-shop"} {
		if !allow[ns] {
			t.Errorf("namespace %s has no list in the golden", ns)
		}
	}

	if len(refused) < 25 || len(admitted) < 15 {
		t.Errorf("the table shrank: %d refusals, %d admissions", len(refused), len(admitted))
	}
}

func variableExpr(t *testing.T, pol policy, name string) string {
	t.Helper()

	for _, v := range pol.Spec.Variables {
		if v.Name == name {
			return v.Expression
		}
	}

	t.Fatalf("policy %s has no variable %s", pol.Metadata.Name, name)

	return ""
}

func TestImageAllowListCanEnforce(t *testing.T) {
	pol, binding := load(t, "image-allow-list-deny")

	if got := strings.Join(binding.Spec.ValidationActions, ","); got != "Deny,Audit" {
		t.Errorf("binding actions %q", got)
	}

	if pol.Metadata.Name != "image-guard" || binding.Metadata.Name != "image-guard" || binding.Spec.PolicyName != "image-guard" {
		t.Errorf("the name is a value: policy %q, binding %q -> %q", pol.Metadata.Name, binding.Metadata.Name, binding.Spec.PolicyName)
	}

	if pol.Spec.FailurePolicy != "Ignore" {
		t.Errorf("failurePolicy %q", pol.Spec.FailurePolicy)
	}

	// No common entries and no kinds: only the row's own list is admitted.
	bad := pod{ns: "shop", containers: []string{"registry.example/mesh/proxy:1"}}
	if got := admitPod(t, pol, "CREATE", "", bad.object(), map[string]any{}); !strings.Contains(got, notListed) {
		t.Errorf("got %q", got)
	}
}
