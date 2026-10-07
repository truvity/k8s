package oidcissuer_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/oidcissuer"
	"github.com/truvity/k8s/pkg/talos/oidc"
)

const issuer = "https://oidc.example.com/clusters/example"

type registration struct {
	typ, name, parent, provider string
	inputs                      resource.PropertyMap
}

type recorder struct {
	mu   sync.Mutex
	regs []registration
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	reg := registration{typ: args.TypeToken, name: args.Name, inputs: args.Inputs}
	if rpc := args.RegisterRPC; rpc != nil {
		reg.parent, reg.provider = rpc.GetParent(), rpc.GetProvider()
	}

	r.mu.Lock()
	r.regs = append(r.regs, reg)
	r.mu.Unlock()

	outs := args.Inputs.Copy()
	if args.TypeToken == "aws:iam/openIdConnectProvider:OpenIdConnectProvider" {
		outs["arn"] = resource.NewStringProperty("arn:example:iam::oidc-provider/oidc.example.com/clusters/example")
	}

	return args.Name + "-id", outs, nil
}

func (*recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) { return args.Args, nil }

func docs(t *testing.T) *oidc.Documents {
	t.Helper()

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	d, err := oidc.Generate(issuer, &k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	return d
}

func run(t *testing.T, a *oidcissuer.Args, withIAM bool) ([]registration, *oidcissuer.OidcIssuer, error) {
	t.Helper()

	rec := &recorder{}

	var comp *oidcissuer.OidcIssuer

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		store, err := pulumiaws.NewProvider(ctx, "store", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		a.ObjectProvider = store

		if withIAM {
			if a.IAMProvider, err = pulumiaws.NewProvider(ctx, "iam", &pulumiaws.ProviderArgs{}); err != nil {
				return err
			}
		}

		comp, err = oidcissuer.New(ctx, "issuer", a)

		return err
	}, pulumi.WithMocks("example", "dev", rec))

	return rec.regs, comp, err
}

func find(t *testing.T, regs []registration, name string) registration {
	t.Helper()

	for _, r := range regs {
		if r.name == name {
			return r
		}
	}

	t.Fatalf("no child %s", name)

	return registration{}
}

func TestObjectsAtTheIssuerPath(t *testing.T) {
	d := docs(t)

	regs, comp, err := run(t, &oidcissuer.Args{Bucket: "issuers", Issuer: issuer, Documents: d}, false)
	if err != nil {
		t.Fatal(err)
	}

	if comp.DiscoveryKey != "clusters/example/.well-known/openid-configuration" || comp.JWKSKey != "clusters/example/openid/v1/jwks" {
		t.Errorf("keys %q %q", comp.DiscoveryKey, comp.JWKSKey)
	}

	disc := find(t, regs, "issuer-discovery")
	jwks := find(t, regs, "issuer-jwks")

	for _, r := range []registration{disc, jwks} {
		if r.typ != "aws:s3/bucketObjectv2:BucketObjectv2" || !strings.HasSuffix(r.parent, "::issuer") || !strings.Contains(r.provider, "::store::") {
			t.Errorf("%s: type %s parent %s provider %s", r.name, r.typ, r.parent, r.provider)
		}

		if r.inputs["contentType"].StringValue() != "application/json" || r.inputs["cacheControl"].StringValue() != "max-age=300" {
			t.Errorf("%s: %v", r.name, r.inputs)
		}

		if _, ok := r.inputs["acl"]; ok {
			t.Errorf("%s: an ACL without being asked for", r.name)
		}
	}

	if disc.inputs["content"].StringValue() != string(d.Discovery) || jwks.inputs["content"].StringValue() != string(d.JWKS) {
		t.Error("the objects do not hold the documents verbatim")
	}

	for _, r := range regs {
		if r.typ == "aws:iam/openIdConnectProvider:OpenIdConnectProvider" {
			t.Error("an IAM provider without IAMProvider")
		}
	}
}

func TestIAMProviderAndOptions(t *testing.T) {
	prefix := "/"

	regs, _, err := run(t, &oidcissuer.Args{
		Bucket: "issuers", Issuer: issuer, Documents: docs(t), KeyPrefix: &prefix, ACL: "public-read",
		CacheControl: "no-cache", Tags: map[string]string{"team": "x"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	if got := find(t, regs, "issuer-jwks").inputs["key"].StringValue(); got != "openid/v1/jwks" {
		t.Errorf("key with an empty prefix %q", got)
	}

	if got := find(t, regs, "issuer-discovery").inputs["acl"].StringValue(); got != "public-read" {
		t.Errorf("acl %q", got)
	}

	p := find(t, regs, "issuer-iam-provider")
	if !strings.Contains(p.provider, "::iam::") || p.inputs["url"].StringValue() != issuer {
		t.Errorf("iam provider %s %v", p.provider, p.inputs["url"])
	}

	if ids := p.inputs["clientIdLists"].ArrayValue(); len(ids) != 1 || ids[0].StringValue() != "sts.amazonaws.com" {
		t.Errorf("client ids %v", ids)
	}
}

func TestRefusals(t *testing.T) {
	other, err := oidc.Generate("https://other.example.com", func() *ecdsa.PublicKey {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		return &k.PublicKey
	}())
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		args oidcissuer.Args
		want string
	}{
		"bucket":    {oidcissuer.Args{Issuer: issuer, Documents: docs(t)}, "Bucket is empty"},
		"issuer":    {oidcissuer.Args{Bucket: "b", Issuer: "http://x", Documents: docs(t)}, "not an https URL"},
		"documents": {oidcissuer.Args{Bucket: "b", Issuer: issuer}, "Documents is empty"},
		"mismatch":  {oidcissuer.Args{Bucket: "b", Issuer: issuer, Documents: other}, "is not Issuer"},
		"audience":  {oidcissuer.Args{Bucket: "b", Issuer: issuer, Documents: docs(t), Audiences: []string{"a", "a"}}, "Audiences[1]"},
	} {
		t.Run(name, func(t *testing.T) {
			a := tc.args

			regs, _, err := run(t, &a, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}

			for _, r := range regs {
				if r.typ != "pulumi:providers:aws" {
					t.Errorf("registered %s before refusing", r.name)
				}
			}
		})
	}
}

func TestTrustPolicy(t *testing.T) {
	arn := "arn:example:iam::oidc-provider/oidc.example.com/clusters/example"

	got, err := oidcissuer.TrustPolicy(arn, issuer, "", "backup/etcd-backup")
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Statement []struct {
			Principal map[string]string
			Action    string
			Condition map[string]map[string]any
		}
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}

	s := doc.Statement[0]
	eq := s.Condition["StringEquals"]

	if s.Principal["Federated"] != arn || s.Action != "sts:AssumeRoleWithWebIdentity" ||
		eq["oidc.example.com/clusters/example:aud"] != "sts.amazonaws.com" ||
		eq["oidc.example.com/clusters/example:sub"].([]any)[0] != "system:serviceaccount:backup:etcd-backup" {
		t.Errorf("policy %s", got)
	}

	wild, err := oidcissuer.TrustPolicy(arn, issuer, "", "apps/*")
	if err != nil || !strings.Contains(wild, `"StringLike":{"oidc.example.com/clusters/example:sub":["system:serviceaccount:apps:*"]}`) {
		t.Errorf("wildcard policy %s %v", wild, err)
	}

	for _, bad := range [][]string{{}, {"no-namespace"}, {"*/sa"}} {
		if _, err := oidcissuer.TrustPolicy(arn, issuer, "", bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
