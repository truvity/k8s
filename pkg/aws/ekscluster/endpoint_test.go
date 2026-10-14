package ekscluster_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/ekscluster"
)

// withEndpoint is base() with the API ingress rules, the API record and the
// node role's registry access.
func withEndpoint() *ekscluster.Args {
	a := base()
	a.APIIngressCIDRs = []string{"10.0.0.0/16", "10.1.0.0/16"}
	a.APIRecord = &ekscluster.APIRecord{ZoneID: "Z123", Name: "api.c1.example"}
	a.NodeECR = &ekscluster.NodeECR{
		CrossAccount: []ekscluster.Registry{{Region: "r-1", AccountID: "acct-a"}},
		PullThroughCache: &ekscluster.PullThroughCache{
			Registry: ekscluster.Registry{Region: "r-2", AccountID: "acct-b"},
			Prefixes: []string{"hub", "quay"},
		},
	}

	return a
}

const (
	sgRule = "aws:ec2/securityGroupRule:SecurityGroupRule"
	record = "aws:route53/record:Record"
	inline = "aws:iam/rolePolicy:RolePolicy"
)

func TestEndpointChildrenDefaultNames(t *testing.T) {
	res, err := run(t, withEndpoint())
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Join(names(res.regs), "\n")

	for _, want := range []string{
		sgRule + " k-api-ingress-0",
		sgRule + " k-api-ingress-1",
		record + " k-api-record",
		inline + " k-node-role-policy-" + ekscluster.NodeECRCrossAccountPolicy,
		inline + " k-node-role-policy-" + ekscluster.NodeECRPullThroughPolicy,
		inline + " k-node-role-policy-pull",
	} {
		if !strings.Contains(got, want+"\n") && !strings.HasSuffix(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

func TestEndpointChildrenAreTheComponentsWithTheLegacyAlias(t *testing.T) {
	res, err := run(t, withEndpoint())
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"k-api-ingress-0", "k-api-ingress-1"} {
		r := find(t, res.regs, sgRule, name)
		if !strings.Contains(r.parent, ekscluster.TypeToken) || len(r.aliases) != 1 || !r.aliases[0].noParent {
			t.Errorf("%s: parent %q aliases %v", name, r.parent, r.aliases)
		}

		if r.protect {
			t.Errorf("%s is protected", name)
		}
	}

	r := find(t, res.regs, record, "k-api-record")
	if !strings.Contains(r.parent, ekscluster.TypeToken) || len(r.aliases) != 1 || !r.aliases[0].noParent {
		t.Errorf("record: parent %q aliases %v", r.parent, r.aliases)
	}
}

func TestAPIIngressRuleInputs(t *testing.T) {
	res, err := run(t, withEndpoint())
	if err != nil {
		t.Fatal(err)
	}

	for name, cidr := range map[string]string{"k-api-ingress-0": "10.0.0.0/16", "k-api-ingress-1": "10.1.0.0/16"} {
		r := find(t, res.regs, sgRule, name)

		if str(r, "type") != "ingress" || str(r, "protocol") != "tcp" || str(r, "description") != ekscluster.APIIngressDescription {
			t.Errorf("%s: %v", name, r.inputs)
		}

		if r.inputs["fromPort"].NumberValue() != 443 || r.inputs["toPort"].NumberValue() != 443 {
			t.Errorf("%s: ports %v", name, r.inputs)
		}

		if got := strs(r, "cidrBlocks"); len(got) != 1 || got[0] != cidr {
			t.Errorf("%s: cidrBlocks %v, want [%s]", name, got, cidr)
		}
	}
}

func TestAPIRecordInputs(t *testing.T) {
	a := withEndpoint()

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	r := find(t, res.regs, record, "k-api-record")
	if str(r, "zoneId") != "Z123" || str(r, "name") != "api.c1.example" || str(r, "type") != "CNAME" {
		t.Errorf("record: %v", r.inputs)
	}

	if r.inputs["ttl"].NumberValue() != ekscluster.DefaultAPIRecordTTL {
		t.Errorf("ttl: %v", r.inputs["ttl"])
	}

	a = withEndpoint()
	a.APIRecord.TTL = 60

	res, err = run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	if ttl := find(t, res.regs, record, "k-api-record").inputs["ttl"].NumberValue(); ttl != 60 {
		t.Errorf("explicit ttl: %v", ttl)
	}
}

// The documents are what aws:iam/getPolicyDocument renders from these
// statements: the mock hands back the statements it was asked for.
func TestNodeECRPolicyStatements(t *testing.T) {
	res, err := run(t, withEndpoint())
	if err != nil {
		t.Fatal(err)
	}

	rec := struct {
		regs    []registration
		invokes []string
	}{res.regs, res.invokes}

	if len(rec.invokes) != 2 {
		t.Fatalf("invokes: %v", rec.invokes)
	}

	type statement struct {
		Sid       string   `json:"sid"`
		Effect    string   `json:"effect"`
		Actions   []string `json:"actions"`
		Resources []string `json:"resources"`
	}

	docs := map[string][]statement{}

	for _, inv := range rec.invokes {
		var doc struct {
			Statements []statement `json:"statements"`
		}
		if err := json.Unmarshal([]byte(inv), &doc); err != nil {
			t.Fatal(err)
		}

		docs[doc.Statements[0].Sid] = doc.Statements
	}

	cross := docs["CrossAccountECRPull"]
	if len(cross) != 2 || cross[0].Resources[0] != "*" || cross[0].Actions[0] != "ecr:GetAuthorizationToken" ||
		cross[1].Sid != "CrossAccountECRImages" || len(cross[1].Actions) != 3 ||
		strings.Join(cross[1].Resources, ",") != "arn:"+ekscluster.DefaultPartition+":ecr:r-1:acct-a:repository/*" {
		t.Errorf("cross-account: %+v", cross)
	}

	ptc := docs["PullThroughCacheImport"]

	arn := "arn:" + ekscluster.DefaultPartition + ":ecr:r-2:acct-b:repository/"
	wantRepos := arn + "hub/*," + arn + "quay/*"

	if len(ptc) != 1 || strings.Join(ptc[0].Actions, ",") != "ecr:BatchImportUpstreamImage,ecr:CreateRepository" ||
		strings.Join(ptc[0].Resources, ",") != wantRepos {
		t.Errorf("pull-through: %+v", ptc)
	}

	for _, r := range children(rec.regs) {
		if r.typ == inline && r.name == "k-node-role-policy-"+ekscluster.NodeECRPullThroughPolicy {
			if str(r, "name") != ekscluster.NodeECRPullThroughPolicy || !strings.Contains(str(r, "policy"), "PullThroughCacheImport") {
				t.Errorf("ptc inline policy: %v", r.inputs)
			}
		}
	}
}

func TestOnlyTheNodeECRPoliciesAsked(t *testing.T) {
	a := base()
	a.NodeECR = &ekscluster.NodeECR{CrossAccount: []ekscluster.Registry{{Region: "r", AccountID: "a"}}}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Join(names(res.regs), "\n")
	if !strings.Contains(got, ekscluster.NodeECRCrossAccountPolicy) || strings.Contains(got, ekscluster.NodeECRPullThroughPolicy) {
		t.Errorf("children:\n%s", got)
	}
}

func TestChildOptionsReachTheirChild(t *testing.T) {
	a := withEndpoint()
	a.ChildOptions = func(c ekscluster.Child) []pulumi.ResourceOption {
		if c.Kind != ekscluster.KindAPIRecord || c.Component != "k" {
			return nil
		}

		return []pulumi.ResourceOption{pulumi.Aliases([]pulumi.Alias{{Name: pulumi.String("old-record"), NoParent: pulumi.Bool(true)}})}
	}

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	r := find(t, res.regs, record, "k-api-record")

	var named, plain bool

	for _, al := range r.aliases {
		named = named || (al.name == "old-record" && al.noParent)
		plain = plain || (al.name == "" && al.noParent)
	}

	if !named || !plain {
		t.Errorf("record aliases %v: want the legacy alias and the hook's", r.aliases)
	}

	for _, c := range children(res.regs) {
		if c.name != "k-api-record" {
			for _, al := range c.aliases {
				if al.name != "" {
					t.Errorf("%s got the record's alias", c.name)
				}
			}
		}
	}
}

func TestEndpointRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(a *ekscluster.Args)
		want   string
	}{
		"bad ingress":       {func(a *ekscluster.Args) { a.APIIngressCIDRs = []string{"nope"} }, "APIIngressCIDRs[0]"},
		"ipv6 ingress":      {func(a *ekscluster.Args) { a.APIIngressCIDRs = []string{"2001:db8::/32"} }, "not IPv4"},
		"ingress host bits": {func(a *ekscluster.Args) { a.APIIngressCIDRs = []string{"10.0.0.1/16"} }, "host bits"},
		"dup ingress":       {func(a *ekscluster.Args) { a.APIIngressCIDRs = []string{"10.0.0.0/16", "10.0.0.0/16"} }, "duplicate"},
		"record zone":       {func(a *ekscluster.Args) { a.APIRecord.ZoneID = "" }, "APIRecord.ZoneID is empty"},
		"record name":       {func(a *ekscluster.Args) { a.APIRecord.Name = "" }, "APIRecord.Name is empty"},
		"record ttl":        {func(a *ekscluster.Args) { a.APIRecord.TTL = -1 }, "negative"},
		"ecr nothing":       {func(a *ekscluster.Args) { a.NodeECR = &ekscluster.NodeECR{} }, "grants nothing"},
		"ecr region":        {func(a *ekscluster.Args) { a.NodeECR.CrossAccount[0].Region = "" }, "CrossAccount[0].Region is empty"},
		"ecr account":       {func(a *ekscluster.Args) { a.NodeECR.PullThroughCache.AccountID = "" }, "PullThroughCache.AccountID is empty"},
		"ecr dup registry": {func(a *ekscluster.Args) {
			a.NodeECR.CrossAccount = append(a.NodeECR.CrossAccount, a.NodeECR.CrossAccount[0])
		}, "duplicate registry"},
		"ptc no prefix":  {func(a *ekscluster.Args) { a.NodeECR.PullThroughCache.Prefixes = nil }, "Prefixes is empty"},
		"ptc bad prefix": {func(a *ekscluster.Args) { a.NodeECR.PullThroughCache.Prefixes = []string{"a/*"} }, "not a repository prefix"},
		"ptc dup prefix": {func(a *ekscluster.Args) { a.NodeECR.PullThroughCache.Prefixes = []string{"a", "a"} }, "duplicate \"a\""},
		"inline clash": {func(a *ekscluster.Args) {
			a.NodeInlinePolicies[0].Name = ekscluster.NodeECRPullThroughPolicy
		}, "name of a NodeECR policy"},
	} {
		t.Run(name, func(t *testing.T) {
			a := withEndpoint()
			tc.mutate(a)

			res, err := run(t, a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}

			if len(children(res.regs)) != 0 {
				t.Errorf("registered %v before refusing", names(res.regs))
			}
		})
	}
}
