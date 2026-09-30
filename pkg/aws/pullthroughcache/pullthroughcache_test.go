package pullthroughcache_test

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/pullthroughcache"
)

type registration struct {
	typ, name, parent string
	noParentAlias     bool
	aliasCount        int
	secretString      string
}

type recorder struct {
	mu   sync.Mutex
	regs []registration
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	reg := registration{typ: args.TypeToken, name: args.Name}

	if rpc := args.RegisterRPC; rpc != nil {
		reg.parent = rpc.GetParent()
		reg.aliasCount = len(rpc.GetAliases()) + len(rpc.GetAliasURNs())

		for _, a := range rpc.GetAliases() {
			if spec := a.GetSpec(); spec != nil && spec.GetNoParent() && spec.GetName() == "" && spec.GetType() == "" {
				reg.noParentAlias = true
			}
		}
	}

	if v, ok := args.Inputs["secretString"]; ok {
		if v.IsSecret() {
			v = v.SecretValue().Element
		}

		if v.IsString() {
			reg.secretString = v.StringValue()
		}
	}

	r.mu.Lock()
	r.regs = append(r.regs, reg)
	r.mu.Unlock()

	return args.Name + "-id", args.Inputs.Copy(), nil
}

func (*recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func run(t *testing.T, args *pullthroughcache.Args) ([]registration, error) {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := pullthroughcache.NewPullThroughCache(ctx, "cache", args)
		return err
	}, pulumi.WithMocks("example", "dev", rec))

	return rec.regs, err
}

func example(legacy bool) *pullthroughcache.Args {
	return &pullthroughcache.Args{
		Upstreams: []pullthroughcache.Upstream{
			{Prefix: "hub", RegistryURL: "registry-1.docker.io", NeedsCredentials: true},
			{Prefix: "gh", RegistryURL: "ghcr.io", NeedsCredentials: true},
			{Prefix: "quay", RegistryURL: "quay.io"},
			{Prefix: "k8s", RegistryURL: "registry.k8s.io"},
		},
		Credentials: map[string]pullthroughcache.Credentials{
			"hub": {Username: pulumi.String("hub-user"), Token: pulumi.String("hub-token")},
			"gh":  {Username: pulumi.String("gh-user"), Token: pulumi.String("gh-token")},
		},
		LegacyTopLevel: legacy,
	}
}

const componentURNType = pullthroughcache.TypeToken

func TestChildrenKeepTheirCurrentNames(t *testing.T) {
	regs, err := run(t, example(true))
	if err != nil {
		t.Fatal(err)
	}

	var got []string

	for _, r := range regs {
		if r.typ == componentURNType {
			continue
		}

		got = append(got, r.typ+" "+r.name)
	}

	sort.Strings(got)

	want := []string{
		"aws:ecr/pullThroughCacheRule:PullThroughCacheRule ptc-rule-gh",
		"aws:ecr/pullThroughCacheRule:PullThroughCacheRule ptc-rule-hub",
		"aws:ecr/pullThroughCacheRule:PullThroughCacheRule ptc-rule-k8s",
		"aws:ecr/pullThroughCacheRule:PullThroughCacheRule ptc-rule-quay",
		"aws:secretsmanager/secret:Secret ptc-secret-gh",
		"aws:secretsmanager/secret:Secret ptc-secret-hub",
		"aws:secretsmanager/secretVersion:SecretVersion ptc-secret-version-gh",
		"aws:secretsmanager/secretVersion:SecretVersion ptc-secret-version-hub",
	}

	if !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}

	if n := countType(regs, componentURNType); n != 1 {
		t.Fatalf("want exactly one component, got %d", n)
	}
}

func countType(regs []registration, typ string) int {
	n := 0

	for _, r := range regs {
		if r.typ == typ {
			n++
		}
	}

	return n
}

func TestLegacyTopLevelAliasesEveryChild(t *testing.T) {
	regs, err := run(t, example(true))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range regs {
		if r.typ == componentURNType {
			continue
		}

		if !strings.Contains(r.parent, "::"+componentURNType+"::cache") {
			t.Errorf("%s %s: parent %q is not the component", r.typ, r.name, r.parent)
		}

		if !r.noParentAlias || r.aliasCount != 1 {
			t.Errorf("%s %s: want exactly one NoParent alias (same name, same type), got noParent=%v count=%d",
				r.typ, r.name, r.noParentAlias, r.aliasCount)
		}
	}
}

func TestWithoutLegacyTopLevelThereAreNoAliases(t *testing.T) {
	regs, err := run(t, example(false))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range regs {
		if r.aliasCount != 0 {
			t.Errorf("%s %s carries %d aliases without LegacyTopLevel", r.typ, r.name, r.aliasCount)
		}
	}
}

func TestSecretPayloadAndName(t *testing.T) {
	regs, err := run(t, example(false))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range regs {
		if r.name != "ptc-secret-version-hub" {
			continue
		}

		var payload map[string]string
		if err := json.Unmarshal([]byte(r.secretString), &payload); err != nil {
			t.Fatalf("payload %q: %v", r.secretString, err)
		}

		if len(payload) != 2 || payload["username"] != "hub-user" || payload["accessToken"] != "hub-token" {
			t.Fatalf("payload = %v", payload)
		}

		return
	}

	t.Fatal("no secret version for hub")
}

func TestNamingHookIsUsedForNameAndAlias(t *testing.T) {
	args := example(true)
	args.Names = func(kind pullthroughcache.Kind, prefix string) string { return string(kind) + "." + prefix }

	regs, err := run(t, args)
	if err != nil {
		t.Fatal(err)
	}

	found := false

	for _, r := range regs {
		if r.name == "rule.quay" {
			found = true

			if !r.noParentAlias {
				t.Error("hooked child lost its alias")
			}
		}
	}

	if !found {
		t.Fatal("rule.quay not registered")
	}
}

func TestRefuses(t *testing.T) {
	cases := map[string]struct {
		mutate func(*pullthroughcache.Args)
		want   string
	}{
		"no upstreams":     {func(a *pullthroughcache.Args) { a.Upstreams, a.Credentials = nil, nil }, "Upstreams is empty"},
		"empty prefix":     {func(a *pullthroughcache.Args) { a.Upstreams[2].Prefix = "" }, "Prefix is empty"},
		"duplicate prefix": {func(a *pullthroughcache.Args) { a.Upstreams[3].Prefix = "quay" }, `duplicate Prefix "quay"`},
		"bad prefix":       {func(a *pullthroughcache.Args) { a.Upstreams[2].Prefix = "Quay!" }, "not a valid ECR repository prefix"},
		"empty url":        {func(a *pullthroughcache.Args) { a.Upstreams[2].RegistryURL = "" }, "RegistryURL is empty"},
		"credentials missing": {
			func(a *pullthroughcache.Args) { delete(a.Credentials, "gh") },
			`upstream "gh" needs credentials`,
		},
		"token missing": {
			func(a *pullthroughcache.Args) {
				a.Credentials["gh"] = pullthroughcache.Credentials{Username: pulumi.String("u")}
			},
			`upstream "gh" needs credentials`,
		},
		"credentials for an open upstream": {
			func(a *pullthroughcache.Args) {
				a.Credentials["quay"] = pullthroughcache.Credentials{Username: pulumi.String("u"), Token: pulumi.String("t")}
			},
			`Credentials["quay"]`,
		},
		"repeated hook name": {
			func(a *pullthroughcache.Args) {
				a.Names = func(pullthroughcache.Kind, string) string { return "same" }
			},
			`Names returned "same" for both`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			args := example(true)
			tc.mutate(args)

			regs, err := run(t, args)
			if err == nil {
				t.Fatal("want a refusal, got nil")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}

			if len(regs) != 0 {
				t.Fatalf("a refused input registered %d resources", len(regs))
			}
		})
	}
}

func TestPrefixesOutput(t *testing.T) {
	var got []string

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		c, err := pullthroughcache.NewPullThroughCache(ctx, "cache", example(false))
		if err != nil {
			return err
		}

		got = c.PrefixList()

		return nil
	}, pulumi.WithMocks("example", "dev", rec))
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"hub", "gh", "quay", "k8s"}; !slices.Equal(got, want) {
		t.Fatalf("prefixes = %v, want %v", got, want)
	}
}
