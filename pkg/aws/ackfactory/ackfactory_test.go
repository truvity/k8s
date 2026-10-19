package ackfactory_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/ackfactory"
)

type registration struct {
	typ, name, parent, importID string
	inputs                      resource.PropertyMap
}

type recorder struct {
	mu   sync.Mutex
	regs []registration
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	reg := registration{typ: args.TypeToken, name: args.Name, inputs: args.Inputs}

	if rpc := args.RegisterRPC; rpc != nil {
		reg.parent = rpc.GetParent()
		reg.importID = rpc.GetImportId()
	}

	r.mu.Lock()
	r.regs = append(r.regs, reg)
	r.mu.Unlock()

	return args.Name + "-id", args.Inputs.Copy(), nil
}

func (*recorder) Call(_ pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{"json": resource.NewProperty(`{"stub":true}`)}, nil
}

var cluster = ackfactory.Cluster{Name: "c1", AccountID: "acct-example", Region: "eu-central-1", Partition: "part"}

type lookup struct {
	policies, roles map[string]bool
	assoc           map[string]string
	err             error
}

func (l lookup) PolicyExists(_ context.Context, arn string) (bool, error) {
	return l.policies[arn], nil
}
func (l lookup) RoleExists(_ context.Context, name string) (bool, error) { return l.roles[name], nil }
func (l lookup) AssociationID(_ context.Context, _, ns, sa string) (string, error) {
	return l.assoc[ns+"/"+sa], l.err
}

func run(t *testing.T, f func(ctx *pulumi.Context, o ackfactory.Options) error) ([]registration, error) {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "aws", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		return f(ctx, ackfactory.Options{Provider: p, ClusterName: pulumi.String("c1"), Boundary: "boundary"})
	}, pulumi.WithMocks("example", "dev", rec))

	return rec.regs, err
}

func names(regs []registration) []string {
	var got []string

	for _, r := range regs {
		if strings.HasPrefix(r.typ, "pulumi:providers:") || strings.HasPrefix(r.typ, "truvity:") {
			continue
		}

		got = append(got, r.typ+" "+r.name)
	}

	sort.Strings(got)

	return got
}

func find(t *testing.T, regs []registration, typ, name string) registration {
	t.Helper()

	for _, r := range regs {
		if r.typ == typ && r.name == name {
			return r
		}
	}

	t.Fatalf("no %s %s registered", typ, name)

	return registration{}
}

func str(r registration, key string) string {
	return r.inputs[resource.PropertyKey(key)].StringValue()
}

// The names are API (docs/decisions/0002): these are the names the stacks
// that adopted the factory already hold in state.
func TestIAMKeepsTheNamesOfTheStacksItWasLiftedFrom(t *testing.T) {
	regs, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		_, err := ackfactory.IAM(ctx, cluster, o, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:eks/podIdentityAssociation:PodIdentityAssociation ack-iam-c1-assoc",
		"aws:iam/role:Role c1-ack-iam-role",
		"aws:iam/rolePolicy:RolePolicy c1-ack-iam-policy",
	}
	if got := names(regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}

	role := find(t, regs, "aws:iam/role:Role", "c1-ack-iam-role")
	if got := str(role, "name"); got != "c1-ack-iam" {
		t.Errorf("role name %q", got)
	}

	if got := str(role, "permissionsBoundary"); got != "arn:part:iam::acct-example:policy/boundary" {
		t.Errorf("boundary %q", got)
	}
}

func TestEKSKeepsTheNamesOfTheStacksItWasLiftedFrom(t *testing.T) {
	regs, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		_, err := ackfactory.EKS(ctx, cluster, o)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:eks/podIdentityAssociation:PodIdentityAssociation ack-eks-c1-assoc",
		"aws:iam/role:Role c1-ack-eks-role",
		"aws:iam/rolePolicy:RolePolicy c1-ack-eks-policy",
	}
	if got := names(regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestServicesDeclareEachControllerAndAdoptWhatIsLive(t *testing.T) {
	l := lookup{
		policies: map[string]bool{"arn:part:iam::acct-example:policy/c1-ack-s3": true},
		roles:    map[string]bool{"c1-ack-s3": true},
		assoc:    map[string]string{"ack-s3/ack-s3-controller": "a-123"},
	}

	regs, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		return ackfactory.Services(ctx, cluster, ackfactory.ServicesOptions{Options: o, Lookup: l})
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{}

	for _, s := range []string{"dynamodb", "kms", "s3"} {
		p := "ack-svc/c1-ack-" + s
		want = append(want,
			"aws:eks/podIdentityAssociation:PodIdentityAssociation "+p+"/pia",
			"aws:iam/policy:Policy "+p+"/policy",
			"aws:iam/role:Role "+p+"/role",
			"aws:iam/rolePolicyAttachment:RolePolicyAttachment "+p+"/attachment",
		)
	}

	sort.Strings(want)

	if got := names(regs); !slices.Equal(got, want) {
		t.Fatalf("children differ.\n got: %q\nwant: %q", got, want)
	}

	if got := find(t, regs, "aws:iam/role:Role", "ack-svc/c1-ack-s3/role").importID; got != "c1-ack-s3" {
		t.Errorf("live s3 role not adopted: import %q", got)
	}

	if got := find(t, regs, "aws:iam/policy:Policy", "ack-svc/c1-ack-s3/policy").importID; got != "arn:part:iam::acct-example:policy/c1-ack-s3" {
		t.Errorf("live s3 policy not adopted: import %q", got)
	}

	if got := find(t, regs, "aws:eks/podIdentityAssociation:PodIdentityAssociation", "ack-svc/c1-ack-s3/pia").importID; got != "c1,a-123" {
		t.Errorf("live s3 association not adopted: import %q", got)
	}

	if got := find(t, regs, "aws:iam/role:Role", "ack-svc/c1-ack-kms/role").importID; got != "" {
		t.Errorf("kms role is not live but import is %q", got)
	}
}

func TestServicesRefuseALookupThatFails(t *testing.T) {
	_, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		return ackfactory.Services(ctx, cluster,
			ackfactory.ServicesOptions{Options: o, Lookup: lookup{err: errors.New("denied")}})
	})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("a failed association lookup must fail the program, got %v", err)
	}
}

func TestProjectRolesAreOneRoleAndOnePolicyPerProject(t *testing.T) {
	regs, err := run(t, func(ctx *pulumi.Context, _ ackfactory.Options) error {
		return ackfactory.ProjectRoles(ctx, cluster, ackfactory.ProjectRolesOptions{Boundary: "deploy"},
			[]ackfactory.ProjectRole{{Name: "alpha"}, {Name: "beta", SSMPrefix: "/custom/beta"}})
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aws:iam/role:Role c1-ack-project-alpha",
		"aws:iam/role:Role c1-ack-project-beta",
		"aws:iam/rolePolicy:RolePolicy c1-ack-project-alpha-policy",
		"aws:iam/rolePolicy:RolePolicy c1-ack-project-beta-policy",
	}
	if got := names(regs); !slices.Equal(got, want) {
		t.Fatalf("resources differ.\n got: %q\nwant: %q", got, want)
	}

	trust := str(find(t, regs, "aws:iam/role:Role", "c1-ack-project-alpha"), "assumeRolePolicy")
	for _, ctl := range []string{"iam", "s3", "kms", "dynamodb"} {
		if !strings.Contains(trust, `"arn:part:iam::acct-example:role/c1-ack-`+ctl+`"`) {
			t.Errorf("trust does not name the %s controller: %s", ctl, trust)
		}
	}

	if got := str(find(t, regs, "aws:iam/rolePolicy:RolePolicy", "c1-ack-project-beta-policy"), "policy"); !strings.Contains(got, "parameter/custom/beta/*") {
		t.Errorf("custom SSM prefix not used: %s", got)
	}

	if got := str(find(t, regs, "aws:iam/rolePolicy:RolePolicy", "c1-ack-project-alpha-policy"), "policy"); !strings.Contains(got, "parameter/business/alpha/*") {
		t.Errorf("default SSM prefix not used: %s", got)
	}

	alpha := str(find(t, regs, "aws:iam/rolePolicy:RolePolicy", "c1-ack-project-alpha-policy"), "policy")
	for _, w := range []string{
		`"kms:ListAliases"`,
		`"kms:CreateAlias", "kms:UpdateAlias", "kms:DeleteAlias"`,
		`:alias/*-alpha-*"`,
	} {
		if !strings.Contains(alpha, w) {
			t.Errorf("KMS alias grant lacks %s: %s", w, alpha)
		}
	}

	if strings.Contains(alpha, `alias/*"`) {
		t.Errorf("alias grant is not project-scoped: %s", alpha)
	}
}

func TestInputsAreRefusedBeforeAnythingIsRegistered(t *testing.T) {
	regs, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		o.Boundary = ""
		_, err := ackfactory.IAM(ctx, ackfactory.Cluster{}, o, false)
		return err
	})
	if err == nil {
		t.Fatal("empty cluster and boundary were accepted")
	}

	for _, want := range []string{"Name is empty", "AccountID is empty", "Region is empty", "Boundary is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not report %q", err, want)
		}
	}

	if got := names(regs); len(got) != 0 {
		t.Errorf("registered %q before refusing", got)
	}
}

func TestThePartitionIsAnInput(t *testing.T) {
	c := cluster
	c.Partition = "part2"

	regs, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		_, err := ackfactory.IAM(ctx, c, o, false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	role := find(t, regs, "aws:iam/role:Role", "c1-ack-iam-role")
	if got := str(role, "permissionsBoundary"); got != "arn:part2:iam::acct-example:policy/boundary" {
		t.Errorf("boundary %q", got)
	}
}

func TestTheDefaultPartitionIsTheCommercialOne(t *testing.T) {
	c := cluster
	c.Partition = ""

	if got, want := c.ARN(), "arn:"+ackfactory.DefaultPartition+":eks:eu-central-1:acct-example:cluster/c1"; got != want {
		t.Errorf("cluster ARN %q, want %q", got, want)
	}
}

func TestProjectRolesCanForbidRolesThePlatformMints(t *testing.T) {
	regs, err := run(t, func(ctx *pulumi.Context, _ ackfactory.Options) error {
		return ackfactory.ProjectRoles(ctx, cluster,
			ackfactory.ProjectRolesOptions{Boundary: "deploy", ForbiddenRoles: []string{"c1-archive-*"}},
			[]ackfactory.ProjectRole{{Name: "alpha"}})
	})
	if err != nil {
		t.Fatal(err)
	}

	got := str(find(t, regs, "aws:iam/rolePolicy:RolePolicy", "c1-ack-project-alpha-policy"), "policy")
	for _, want := range []string{`"Effect": "Deny"`, `"iam:*"`, `arn:part:iam::acct-example:role/c1-archive-*`} {
		if !strings.Contains(got, want) {
			t.Errorf("policy lacks %s: %s", want, got)
		}
	}

	// And with none, the allow-only document is untouched.
	plain, err := run(t, func(ctx *pulumi.Context, _ ackfactory.Options) error {
		return ackfactory.ProjectRoles(ctx, cluster, ackfactory.ProjectRolesOptions{Boundary: "deploy"},
			[]ackfactory.ProjectRole{{Name: "alpha"}})
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(str(find(t, plain, "aws:iam/rolePolicy:RolePolicy", "c1-ack-project-alpha-policy"), "policy"), "Deny") {
		t.Error("a policy with no forbidden roles carries a deny")
	}
}

var update = flag.Bool("update", false, "rewrite testdata/trust-*.json")

// golden compares a trust policy with testdata/trust-<name>.json, byte for
// byte: the provider compares the string, so any change to it is an update
// of the live role.
func golden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", "trust-"+name+".json")
	if *update {
		if err := os.WriteFile(path, []byte(got+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if got != strings.TrimSuffix(string(want), "\n") {
		t.Errorf("%s differs (run with -update and review):\n got: %s\nwant: %s", path, got, want)
	}
}

// trustConditions returns the StringEquals and ArnEquals conditions of a
// single-statement trust policy.
func trustConditions(t *testing.T, doc string) (stringEquals, arnEquals map[string]any) {
	t.Helper()

	var policy struct {
		Statement []struct {
			Condition map[string]map[string]any
		}
	}

	if err := json.Unmarshal([]byte(doc), &policy); err != nil {
		t.Fatalf("trust policy is not JSON: %v: %s", err, doc)
	}

	if len(policy.Statement) != 1 {
		t.Fatalf("trust policy has %d statements, want 1: %s", len(policy.Statement), doc)
	}

	c := policy.Statement[0].Condition

	return c["StringEquals"], c["ArnEquals"]
}

// The controller roles trust only their own controller's service account:
// without the namespace and service-account conditions, any Pod Identity
// association in the cluster could name the role, and the EKS controller may
// pass every role named after the cluster.
func TestIAMAndEKSTrustOnlyTheirControllerServiceAccount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		deploy func(*pulumi.Context, ackfactory.Options) error
	}{
		{"iam", func(ctx *pulumi.Context, o ackfactory.Options) error {
			_, err := ackfactory.IAM(ctx, cluster, o, true)
			return err
		}},
		{"eks", func(ctx *pulumi.Context, o ackfactory.Options) error {
			_, err := ackfactory.EKS(ctx, cluster, o)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			regs, err := run(t, tc.deploy)
			if err != nil {
				t.Fatal(err)
			}

			ns, sa := "ack-"+tc.name, "ack-"+tc.name+"-controller"
			trust := str(find(t, regs, "aws:iam/role:Role", "c1-ack-"+tc.name+"-role"), "assumeRolePolicy")

			stringEquals, arnEquals := trustConditions(t, trust)
			for k, want := range map[string]string{
				"aws:SourceAccount":                         "acct-example",
				"aws:RequestTag/kubernetes-namespace":       ns,
				"aws:RequestTag/kubernetes-service-account": sa,
			} {
				if got := stringEquals[k]; got != want {
					t.Errorf("StringEquals %s = %v, want %q: %s", k, got, want, trust)
				}
			}

			if got, want := arnEquals["aws:SourceArn"], cluster.ARN(); got != want {
				t.Errorf("ArnEquals aws:SourceArn = %v, want %q: %s", got, want, trust)
			}

			// The association binds the same pair the trust names.
			assoc := find(t, regs, "aws:eks/podIdentityAssociation:PodIdentityAssociation", "ack-"+tc.name+"-c1-assoc")
			if got := str(assoc, "namespace"); got != ns {
				t.Errorf("association namespace %q, want %q", got, ns)
			}

			if got := str(assoc, "serviceAccount"); got != sa {
				t.Errorf("association service account %q, want %q", got, sa)
			}

			golden(t, tc.name, trust)
		})
	}
}

// The service controllers' trust documents are pinned: they already carried
// the namespace and service-account conditions, and a re-rendered string is
// an update of every live role.
func TestServicesTrustIsUnchanged(t *testing.T) {
	regs, err := run(t, func(ctx *pulumi.Context, o ackfactory.Options) error {
		return ackfactory.Services(ctx, cluster, ackfactory.ServicesOptions{Options: o, Lookup: lookup{}})
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range []string{"s3", "kms", "dynamodb"} {
		golden(t, s, str(find(t, regs, "aws:iam/role:Role", "ack-svc/c1-ack-"+s+"/role"), "assumeRolePolicy"))
	}
}
