package podidentity_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/podidentity"
)

const (
	issuer   = "https://issuer.example/cluster"
	hostPath = "issuer.example/cluster"
)

// irsa is base() in IRSA mode: no cluster, no region, no association tags,
// an issuer and the account the provider lives in.
func irsa() *podidentity.Args {
	a := base()
	a.Mode = podidentity.ModeIRSA
	a.Issuer = issuer
	a.ClusterName, a.ClusterARN, a.Region = nil, "", ""

	return a
}

// providerARN builds the OIDC provider's ARN from a partition, as the
// repository's tests build every ARN (the leak canary bans a literal one).
func providerARN(partition string) string {
	return "arn:" + partition + ":iam::" + account + ":oidc-provider/" + hostPath
}

type federatedTrust struct {
	Statement []struct {
		Effect    string
		Principal map[string]string
		Action    any
		Condition map[string]map[string]any
	}
}

func trustOf(t *testing.T, a *podidentity.Args) federatedTrust {
	t.Helper()

	res, err := run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	var doc federatedTrust
	if err := json.Unmarshal([]byte(str(find(t, res.regs, "aws:iam/role:Role", "ident-role"), "assumeRolePolicy")), &doc); err != nil {
		t.Fatal(err)
	}

	if len(doc.Statement) != 1 {
		t.Fatalf("want one statement, got %d", len(doc.Statement))
	}

	return doc
}

func TestIRSATrustIsFederatedAndScopedToTheServiceAccounts(t *testing.T) {
	a := irsa()
	a.ServiceAccounts = []string{"zeta", "alpha"}

	s := trustOf(t, a).Statement[0]

	if s.Effect != "Allow" || s.Principal["Federated"] != providerARN("aws") || len(s.Principal) != 1 {
		t.Errorf("statement = %+v", s)
	}

	if s.Action != "sts:AssumeRoleWithWebIdentity" {
		t.Errorf("action = %v", s.Action)
	}

	if got := s.Condition["StringEquals"][hostPath+":aud"]; got != "sts.amazonaws.com" {
		t.Errorf("aud = %v", got)
	}

	var subs []string
	for _, v := range s.Condition["StringEquals"][hostPath+":sub"].([]any) {
		subs = append(subs, v.(string))
	}

	if want := []string{"system:serviceaccount:team-a:zeta", "system:serviceaccount:team-a:alpha"}; !slices.Equal(subs, want) {
		t.Errorf("sub = %v, want %v in the order given", subs, want)
	}

	if len(s.Condition) != 1 {
		t.Errorf("conditions = %v: the Pod Identity source conditions do not belong here", s.Condition)
	}
}

func TestIRSATrustUsesTheProviderARNItIsGiven(t *testing.T) {
	a := irsa()
	a.AccountID = ""
	a.ProviderARN = providerARN("example-given")

	if got := trustOf(t, a).Statement[0].Principal["Federated"]; got != providerARN("example-given") {
		t.Errorf("federated = %q", got)
	}
}

func TestIRSAPartitionFlowsIntoTheDerivedProviderARN(t *testing.T) {
	a := irsa()
	a.Partition = "example-gov"

	if got := trustOf(t, a).Statement[0].Principal["Federated"]; got != providerARN("example-gov") {
		t.Errorf("federated = %q, want %q", got, providerARN("example-gov"))
	}
}

func TestIRSAAnIssuerWithoutAPathUsesTheHostAlone(t *testing.T) {
	a := irsa()
	a.Issuer = "https://issuer.example"

	s := trustOf(t, a).Statement[0]
	if want := "arn:" + "aws" + ":iam::" + account + ":oidc-provider/issuer.example"; s.Principal["Federated"] != want {
		t.Errorf("federated = %q", s.Principal["Federated"])
	}

	if _, ok := s.Condition["StringEquals"]["issuer.example:aud"]; !ok {
		t.Errorf("conditions = %v", s.Condition)
	}
}

func TestIRSARegistersNoAssociation(t *testing.T) {
	res, err := run(t, irsa())
	if err != nil {
		t.Fatal(err)
	}

	got := names(res.regs)
	want := []string{
		"aws:iam/policy:Policy ident-policy",
		"aws:iam/role:Role ident-role",
		"aws:iam/rolePolicy:RolePolicy ident-role-policy",
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment ident-attachment",
	}

	if !slices.Equal(got, want) {
		t.Errorf("children = %v, want %v", got, want)
	}
}

func TestIRSAKeepsTheDefaultChildNamesAndInputs(t *testing.T) {
	res, err := run(t, irsa())
	if err != nil {
		t.Fatal(err)
	}

	role := find(t, res.regs, "aws:iam/role:Role", "ident-role")
	if str(role, "name") != "c1-app" || str(role, "permissionsBoundary") != "boundary-arn" {
		t.Errorf("role inputs = %v", role.inputs)
	}

	if got := str(find(t, res.regs, "aws:iam/rolePolicy:RolePolicy", "ident-role-policy"), "policy"); got != `{"inline":true}` {
		t.Errorf("inline policy = %s", got)
	}
}

func TestIRSAProtectsTheRoleAndPolicyOnlyWhenAsked(t *testing.T) {
	res, err := run(t, irsa())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		if r.protect {
			t.Errorf("%s is protected by default", r.name)
		}
	}

	a := irsa()
	a.Protect = true

	res, err = run(t, a)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range children(res.regs) {
		want := r.name == "ident-role" || r.name == "ident-policy"
		if r.protect != want {
			t.Errorf("%s protect = %v, want %v", r.name, r.protect, want)
		}
	}
}

func TestIRSATrustPolicyOverrideIsUsedVerbatim(t *testing.T) {
	for name, mode := range map[string]podidentity.Mode{"irsa": podidentity.ModeIRSA, "pod identity": podidentity.ModePodIdentity} {
		t.Run(name, func(t *testing.T) {
			a := irsa()
			a.Mode = mode
			a.TrustPolicy = pulumi.String("  {\"verbatim\": 1}\n")
			a.AccountID, a.ClusterARN, a.Issuer = "", "", ""

			if mode == podidentity.ModePodIdentity {
				a.ClusterName = pulumi.String("c1")
			}

			res, err := run(t, a)
			if err != nil {
				t.Fatal(err)
			}

			if got := str(find(t, res.regs, "aws:iam/role:Role", "ident-role"), "assumeRolePolicy"); got != "  {\"verbatim\": 1}\n" {
				t.Errorf("trust = %q", got)
			}
		})
	}
}

func TestIRSAAWildcardServiceAccountIsRefusedUnlessTheCallerWritesTheTrust(t *testing.T) {
	a := irsa()
	a.ServiceAccounts = []string{"app-*"}

	_, err := run(t, a)
	if err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("err = %v, want a refusal of the wildcard", err)
	}

	a.TrustPolicy = pulumi.String(`{"written":"by the caller"}`)

	if _, err := run(t, a); err != nil {
		t.Errorf("a caller-written trust may name a wildcard: %v", err)
	}
}

func TestIRSARefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(a *podidentity.Args)
		want   string
	}{
		"unknown mode":          {func(a *podidentity.Args) { a.Mode = "web-identity" }, `unknown Mode "web-identity"`},
		"no issuer":             {func(a *podidentity.Args) { a.Issuer = "" }, "Issuer is empty"},
		"http issuer":           {func(a *podidentity.Args) { a.Issuer = "http://issuer.example/cluster" }, "Issuer"},
		"no scheme":             {func(a *podidentity.Args) { a.Issuer = "issuer.example/cluster" }, "Issuer"},
		"no host":               {func(a *podidentity.Args) { a.Issuer = "https:///cluster" }, "Issuer"},
		"trailing slash":        {func(a *podidentity.Args) { a.Issuer = issuer + "/" }, "trailing slash"},
		"query":                 {func(a *podidentity.Args) { a.Issuer = issuer + "?x=1" }, "Issuer"},
		"no account, no ARN":    {func(a *podidentity.Args) { a.AccountID = "" }, "AccountID is empty and no ProviderARN"},
		"wildcard":              {func(a *podidentity.Args) { a.ServiceAccounts = []string{"a*"} }, "wildcard"},
		"role name":             {func(a *podidentity.Args) { a.RoleName = "" }, "RoleName is empty"},
		"namespace":             {func(a *podidentity.Args) { a.Namespace = "" }, "Namespace is empty"},
		"no accounts":           {func(a *podidentity.Args) { a.ServiceAccounts = nil }, "ServiceAccounts is empty"},
		"cluster name is moot":  {func(a *podidentity.Args) { a.ClusterName = pulumi.String("c1") }, "ClusterName is set"},
		"cluster arn is moot":   {func(a *podidentity.Args) { a.ClusterARN = cluster }, "ClusterARN is set"},
		"region is moot":        {func(a *podidentity.Args) { a.Region = "eu-central-1" }, "Region is set"},
		"ignored tags is moot":  {func(a *podidentity.Args) { a.IgnoreAssociationTags = true }, "IgnoreAssociationTags is set"},
		"association imports":   {func(a *podidentity.Args) { a.Imports.Associations = map[string]string{"app": "c1,a"} }, "Imports.Associations is set"},
		"inline policy name":    {func(a *podidentity.Args) { a.InlinePolicy.Name = "" }, "InlinePolicy.Name"},
		"repeated name":         {func(a *podidentity.Args) { a.Names = func(podidentity.Child) string { return "same" } }, "for both"},
		"empty service account": {func(a *podidentity.Args) { a.ServiceAccounts = []string{""} }, "ServiceAccounts[0] is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			a := irsa()
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

func TestPodIdentityModeRefusesWhatOnlyIRSAReads(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(a *podidentity.Args)
		want   string
	}{
		"issuer":       {func(a *podidentity.Args) { a.Issuer = issuer }, "Issuer is set"},
		"provider arn": {func(a *podidentity.Args) { a.ProviderARN = providerARN("aws") }, "ProviderARN is set"},
		"partition":    {func(a *podidentity.Args) { a.Partition = "aws" }, "Partition is set"},
	} {
		for label, mode := range map[string]podidentity.Mode{"the default mode": podidentity.ModePodIdentity, "a mode spelled pod-identity": "pod-identity"} {
			t.Run(name+", "+label, func(t *testing.T) {
				a := base()
				a.Mode = mode
				tc.mutate(a)

				if mode != podidentity.ModePodIdentity {
					// Only the empty mode is the Pod Identity mode.
					if _, err := run(t, a); err == nil || !strings.Contains(err.Error(), "unknown Mode") {
						t.Fatalf("err = %v, want an unknown Mode", err)
					}

					return
				}

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
}

func TestIRSARefusalReportsEveryProblemAtOnce(t *testing.T) {
	a := irsa()
	a.Issuer = "http://issuer.example/"
	a.AccountID = ""
	a.ClusterName = pulumi.String("c1")
	a.Region = "eu-central-1"
	a.IgnoreAssociationTags = true

	_, err := run(t, a)
	if err == nil {
		t.Fatal("want an error")
	}

	for _, want := range []string{"Issuer", "AccountID is empty and no ProviderARN", "ClusterName is set", "Region is set", "IgnoreAssociationTags is set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestRoleARNAnnotationIsTheOneTheWebhookReads(t *testing.T) {
	if podidentity.RoleARNAnnotation != "eks.amazonaws.com/role-arn" {
		t.Errorf("RoleARNAnnotation = %q", podidentity.RoleARNAnnotation)
	}
}
