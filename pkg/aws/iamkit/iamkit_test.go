package iamkit_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/iamkit"
)

type registration struct {
	typ, name, parent, provider string
	ignore                      []string
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
		reg.provider = rpc.GetProvider()
		reg.ignore = rpc.GetIgnoreChanges()
	}

	r.mu.Lock()
	r.regs = append(r.regs, reg)
	r.mu.Unlock()

	outs := args.Inputs.Copy()
	outs["arn"] = resource.NewStringProperty("arn-of-" + args.Name)

	return args.Name + "-id", outs, nil
}

func (*recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func run(t *testing.T, boundary string, program func(iamkit.Kit) error) []registration {
	t.Helper()

	return runKit(t, iamkit.Kit{Boundary: boundary}, program)
}

// runKit completes kit's context, provider and prefix.
func runKit(t *testing.T, kit iamkit.Kit, program func(iamkit.Kit) error) []registration {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		provider, err := pulumiaws.NewProvider(ctx, "aws", &pulumiaws.ProviderArgs{Region: pulumi.String("eu-central-1")})
		if err != nil {
			return err
		}

		kit.Ctx, kit.Provider, kit.Prefix = ctx, provider, "ids/"

		return program(kit)
	}, pulumi.WithMocks("project", "stack", rec))
	if err != nil {
		t.Fatal(err)
	}

	var out []registration

	for _, r := range rec.regs {
		if r.typ != "pulumi:providers:aws" {
			out = append(out, r)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })

	return out
}

func str(r registration, key resource.PropertyKey) string {
	v, ok := r.inputs[key]
	if !ok || !v.IsString() {
		return ""
	}

	return v.StringValue()
}

// The kit's logical names are the prefix and the caller's name, nothing
// else: a program that registered the same objects by hand keeps its URNs.
func TestKitNamesAndInputs(t *testing.T) {
	t.Parallel()

	doc := iamkit.MustJSON(map[string]any{
		iamkit.Version:   iamkit.DocVersion,
		iamkit.Statement: []map[string]any{{iamkit.Effect: iamkit.Allow, iamkit.Action: "s3:GetObject", iamkit.Resource: "*"}},
	})

	trust := iamkit.MustJSON(map[string]any{
		iamkit.Version: iamkit.DocVersion,
		iamkit.Statement: []map[string]any{{
			iamkit.Effect:    iamkit.Allow,
			iamkit.Principal: map[string]any{iamkit.Service: "ec2.amazonaws.com"},
			iamkit.Action:    []string{iamkit.AssumeRole, iamkit.TagSession},
		}},
	})

	regs := run(t, "boundary-arn", func(k iamkit.Kit) error {
		policy, err := k.Policy("reader", "Reads one bucket", doc)
		if err != nil {
			return err
		}

		if _, err := k.Role("worker", pulumi.String(trust), policy); err != nil {
			return err
		}

		bare, err := k.BareRole("bare", pulumi.String(trust))
		if err != nil {
			return err
		}

		return k.Attach("bare/reader", bare.Name, policy)
	})

	got := make([]string, 0, len(regs))
	for _, r := range regs {
		got = append(got, r.typ+" "+r.name)
	}

	want := []string{
		"aws:iam/role:Role ids/bare",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment ids/bare/reader",
		"aws:iam/policy:Policy ids/reader",
		"aws:iam/role:Role ids/worker",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment ids/worker/attachment",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations:\n got %v\nwant %v", got, want)
	}

	for _, r := range regs {
		if r.parent != "" && r.parent != "urn:pulumi:stack::project::pulumi:pulumi:Stack::project-stack" {
			t.Errorf("%s: parent %q, want the stack", r.name, r.parent)
		}

		if r.provider == "" {
			t.Errorf("%s: registered without the kit's provider", r.name)
		}

		switch r.typ {
		case "aws:iam/policy:Policy":
			if str(r, "name") != "reader" || str(r, "description") != "Reads one bucket" || str(r, "policy") != doc {
				t.Errorf("policy inputs: %v", r.inputs)
			}
		case "aws:iam/role:Role":
			if str(r, "permissionsBoundary") != "boundary-arn" || str(r, "assumeRolePolicy") != trust {
				t.Errorf("%s inputs: %v", r.name, r.inputs)
			}
		}
	}
}

// IAM cannot edit a policy's description, so a changed one would replace the
// policy and detach it from its roles for a window. The kit registers a
// policy with IgnoreChanges on the description (the engine then reports no
// diff for it), leaves the roles alone (a role's description is not
// immutable and the kit sets none), and TrackDescription is the way out.
func TestPolicyIgnoresDescriptionChanges(t *testing.T) {
	t.Parallel()

	program := func(k iamkit.Kit) error {
		policy, err := k.Policy("reader", "Reads one bucket", "{}")
		if err != nil {
			return err
		}

		_, err = k.Role("worker", pulumi.String("{}"), policy)

		return err
	}

	for _, tc := range []struct {
		name   string
		track  bool
		ignore []string
	}{
		{"default", false, []string{"description"}},
		{"opt-out", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			regs := runKit(t, iamkit.Kit{TrackDescription: tc.track}, program)

			for _, r := range regs {
				want := []string(nil)
				if r.typ == "aws:iam/policy:Policy" {
					want = tc.ignore
				}

				if !reflect.DeepEqual(r.ignore, want) {
					t.Errorf("%s %s: ignoreChanges %v, want %v", r.typ, r.name, r.ignore, want)
				}
			}

			// The description is still an input: a new policy gets it.
			for _, r := range regs {
				if r.typ == "aws:iam/policy:Policy" && str(r, "description") != "Reads one bucket" {
					t.Errorf("policy description input: %v", r.inputs)
				}
			}
		})
	}
}

func TestKitWithoutBoundarySetsNone(t *testing.T) {
	t.Parallel()

	regs := run(t, "", func(k iamkit.Kit) error {
		_, err := k.BareRole("bare", pulumi.String("{}"))

		return err
	})

	if len(regs) != 1 {
		t.Fatalf("registrations: %v", regs)
	}

	if _, set := regs[0].inputs["permissionsBoundary"]; set {
		t.Fatalf("an empty Boundary must set none: %v", regs[0].inputs)
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()

	got := iamkit.MustJSON(map[string]any{iamkit.Version: iamkit.DocVersion, iamkit.Statement: []any{}})

	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil || back[iamkit.Version] != iamkit.DocVersion {
		t.Fatalf("got %s (%v)", got, err)
	}

	if _, err := iamkit.JSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("an unrenderable document must be an error")
	}

	defer func() {
		if recover() == nil {
			t.Fatal("MustJSON of an unrenderable document must panic")
		}
	}()

	iamkit.MustJSON(map[string]any{"bad": make(chan int)})
}
