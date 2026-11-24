package ackfactory_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/ackfactory"
)

// The builder name has two shapes. The default is what every stack holds; the
// dotted one is new and optional.
func TestBuilderNameHasTwoShapes(t *testing.T) {
	cases := map[string]struct {
		p    ackfactory.ProjectRole
		want string
	}{
		"default":          {ackfactory.ProjectRole{Name: "shop"}, "c1-ack-project-shop"},
		"default with ssm": {ackfactory.ProjectRole{Name: "shop", SSMPrefix: "/x", Slug: "s"}, "c1-ack-project-shop"},
		"dotted":           {ackfactory.ProjectRole{Name: "shop", Vendor: "acme", Customer: "eu"}, "c1-acme-shop-eu.ack-project"},
		"dotted with part": {ackfactory.ProjectRole{Name: "shop", Vendor: "acme", Customer: "eu", Part: "api"}, "c1-acme-shop-eu-api.ack-project"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.p.BuilderName(cluster); got != tc.want {
				t.Errorf("BuilderName = %q, want %q", got, tc.want)
			}
		})
	}
}

func projectRolesRun(t *testing.T, o ackfactory.ProjectRolesOptions, projects ...ackfactory.ProjectRole) ([]registration, error) {
	t.Helper()

	if o.Boundary == "" {
		o.Boundary = "deploy"
	}

	return run(t, func(ctx *pulumi.Context, _ ackfactory.Options) error {
		return ackfactory.ProjectRoles(ctx, cluster, o, projects)
	})
}

// The old shape's URNs are API: a project that gains a dotted builder keeps the
// resources of its default builder under the very names it had, and byte for
// byte the same inputs.
func TestDottedBuilderBesideTheDefaultKeepsTheDefaultsURNs(t *testing.T) {
	only, err := projectRolesRun(t, ackfactory.ProjectRolesOptions{}, ackfactory.ProjectRole{Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}

	wantOnly := []string{
		"aws:iam/role:Role c1-ack-project-shop",
		"aws:iam/rolePolicy:RolePolicy c1-ack-project-shop-policy",
	}
	if got := names(only); !slices.Equal(got, wantOnly) {
		t.Fatalf("default builder registers %q, want %q", got, wantOnly)
	}

	both, err := projectRolesRun(t, ackfactory.ProjectRolesOptions{},
		ackfactory.ProjectRole{Name: "shop"},
		ackfactory.ProjectRole{Name: "shop", Vendor: "acme", Customer: "eu"},
		ackfactory.ProjectRole{Name: "shop", Vendor: "acme", Customer: "eu", Part: "api"},
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:iam/role:Role c1-ack-project-shop",
		"aws:iam/role:Role c1-acme-shop-eu-api.ack-project",
		"aws:iam/role:Role c1-acme-shop-eu.ack-project",
		"aws:iam/rolePolicy:RolePolicy c1-ack-project-shop-policy",
		"aws:iam/rolePolicy:RolePolicy c1-acme-shop-eu-api.ack-project-policy",
		"aws:iam/rolePolicy:RolePolicy c1-acme-shop-eu.ack-project-policy",
	}
	if got := names(both); !slices.Equal(got, want) {
		t.Fatalf("resources differ.\n got: %q\nwant: %q", got, want)
	}

	// The default builder's inputs did not move.
	for _, kind := range [][2]string{
		{"aws:iam/role:Role", "c1-ack-project-shop"},
		{"aws:iam/rolePolicy:RolePolicy", "c1-ack-project-shop-policy"},
	} {
		a, b := find(t, only, kind[0], kind[1]), find(t, both, kind[0], kind[1])
		if !a.inputs.DeepEquals(b.inputs) {
			t.Errorf("%s %s changed:\n before: %v\n  after: %v", kind[0], kind[1], a.inputs, b.inputs)
		}
	}

	role := find(t, both, "aws:iam/role:Role", "c1-acme-shop-eu.ack-project")
	if got := str(role, "name"); got != "c1-acme-shop-eu.ack-project" {
		t.Errorf("role name %q", got)
	}

	// Same tags, same boundary and same trust as the default shape: only the
	// name differs.
	def := find(t, both, "aws:iam/role:Role", "c1-ack-project-shop")
	for _, key := range []string{"tags", "permissionsBoundary", "assumeRolePolicy"} {
		if !def.inputs[resource.PropertyKey(key)].DeepEquals(role.inputs[resource.PropertyKey(key)]) {
			t.Errorf("%s differs between the shapes", key)
		}
	}

	pol := find(t, both, "aws:iam/rolePolicy:RolePolicy", "c1-acme-shop-eu.ack-project-policy")
	if got := str(pol, "role"); got == "" {
		t.Error("the dotted policy names no role")
	}

	if got := str(pol, "name"); got != "c1-acme-shop-eu.ack-project-policy" {
		t.Errorf("policy name %q", got)
	}
}

// Apart from the name the dotted builder's policy is the default one, and
// with ProtectTags its "touch the role itself" deny names its own role.
func TestDottedBuilderPolicy(t *testing.T) {
	policyOf := func(o ackfactory.ProjectRolesOptions, p ackfactory.ProjectRole) string {
		t.Helper()

		regs, err := projectRolesRun(t, o, p)
		if err != nil {
			t.Fatal(err)
		}

		return str(find(t, regs, "aws:iam/rolePolicy:RolePolicy", p.BuilderName(cluster)+"-policy"), "policy")
	}

	dotted := ackfactory.ProjectRole{Name: "alpha", Vendor: "acme", Customer: "eu"}

	plain := ackfactory.ProjectRolesOptions{}

	if got, want := policyOf(plain, dotted), policyOf(plain, ackfactory.ProjectRole{Name: "alpha"}); got != want {
		t.Errorf("default-options policy differs between the shapes:\n got: %s\nwant: %s", got, want)
	}

	tight := ackfactory.ProjectRolesOptions{Tighten: true, PodIdentity: true, WholeNames: true, BucketPrefix: "acme-"}

	deflt := policyOf(tight, ackfactory.ProjectRole{Name: "alpha"})
	if !strings.Contains(deflt, `"Resource": "arn:part:iam::acct-example:role/c1-ack-project-alpha"`) {
		t.Errorf("the default builder's own-role deny moved: %s", deflt)
	}

	got := policyOf(tight, dotted)
	if !strings.Contains(got, `"Resource": "arn:part:iam::acct-example:role/c1-acme-alpha-eu.ack-project"`) {
		t.Errorf("the dotted builder's own-role deny does not name it: %s", got)
	}

	if strings.Contains(got, "role/c1-ack-project-alpha") {
		t.Errorf("the dotted builder's policy names the default shape: %s", got)
	}

	goldenFile(t, "policy-project-dotted-tight", got)
}

func TestDottedBuilderNamesAreRefusedWhenTheyCannotWork(t *testing.T) {
	long := strings.Repeat("a", 30)

	for name, tc := range map[string]struct {
		p    ackfactory.ProjectRole
		want string
	}{
		"vendor without customer": {ackfactory.ProjectRole{Name: "shop", Vendor: "acme"}, "are set together"},
		"customer without vendor": {ackfactory.ProjectRole{Name: "shop", Customer: "eu"}, "are set together"},
		"part alone":              {ackfactory.ProjectRole{Name: "shop", Part: "api"}, "are set together"},
		"upper case":              {ackfactory.ProjectRole{Name: "shop", Vendor: "Acme", Customer: "eu"}, "Vendor"},
		"a dot in the customer":   {ackfactory.ProjectRole{Name: "shop", Vendor: "acme", Customer: "e.u"}, "Customer"},
		"a leading hyphen":        {ackfactory.ProjectRole{Name: "shop", Vendor: "acme", Customer: "eu", Part: "-api"}, "Part"},
		"a trailing hyphen":       {ackfactory.ProjectRole{Name: "shop-", Vendor: "acme", Customer: "eu"}, "Name"},
		"a wildcard":              {ackfactory.ProjectRole{Name: "sh*p", Vendor: "acme", Customer: "eu"}, "Name"},
		"a dotted name":           {ackfactory.ProjectRole{Name: "sh.op", Vendor: "acme", Customer: "eu"}, "Name"},
		"over 64 characters":      {ackfactory.ProjectRole{Name: long, Vendor: long, Customer: "eu"}, "limit is 64"},
	} {
		t.Run(name, func(t *testing.T) {
			regs, err := projectRolesRun(t, ackfactory.ProjectRolesOptions{}, tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}

			if got := names(regs); len(got) != 0 {
				t.Errorf("a refused input registered %q", got)
			}
		})
	}

	// 64 characters exactly is fine.
	fit := ackfactory.ProjectRole{Name: strings.Repeat("a", 20), Vendor: strings.Repeat("b", 20), Customer: strings.Repeat("c", 7)}
	if n := len(fit.BuilderName(cluster)); n != 64 {
		t.Fatalf("test input is %d characters, want 64", n)
	}

	if _, err := projectRolesRun(t, ackfactory.ProjectRolesOptions{}, fit); err != nil {
		t.Errorf("a 64-character name was refused: %v", err)
	}
}

func TestTwoEntriesWithTheSameRoleNameAreRefused(t *testing.T) {
	for name, projects := range map[string][]ackfactory.ProjectRole{
		"default twice": {{Name: "shop"}, {Name: "shop", SSMPrefix: "/x"}},
		"dotted twice": {
			{Name: "shop", Vendor: "acme", Customer: "eu"},
			{Name: "shop", Vendor: "acme", Customer: "eu"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := projectRolesRun(t, ackfactory.ProjectRolesOptions{}, projects...)
			if err == nil || !strings.Contains(err.Error(), "listed twice") {
				t.Fatalf("err = %v, want a duplicate refusal", err)
			}
		})
	}
}

// Every controller may assume both shapes; the default pattern stays first.
func TestControllersMayAssumeBothBuilderShapes(t *testing.T) {
	want := []string{
		"arn:part:iam::acct-example:role/c1-ack-project-*",
		"arn:part:iam::acct-example:role/c1-*.ack-project",
	}

	rec, err := runRecorded(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		_, err := ackfactory.IAM(ctx, cluster, o, true)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := policyStatements(t, rec)["AssumeProjectRoles"].Resources; !slices.Equal(got, want) {
		t.Errorf("IAM assume resources %q, want %q", got, want)
	}

	regs, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		return ackfactory.Services(ctx, cluster, ackfactory.ServicesOptions{Options: o, Lookup: lookup{}})
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{"s3", "kms", "dynamodb"} {
		doc := str(find(t, regs, "aws:iam/policy:Policy", "ack-svc/c1-ack-"+svc+"/policy"), "policy")
		for _, w := range want {
			if !strings.Contains(doc, `"`+w+`"`) {
				t.Errorf("%s policy does not allow assuming %s: %s", svc, w, doc)
			}
		}
	}
}
