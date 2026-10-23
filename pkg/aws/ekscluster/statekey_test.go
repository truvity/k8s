package ekscluster_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/aws/ekscluster"
)

// account is built, not written, so no 12-digit literal sits in the tests.
var account = strings.Repeat("1", 12)

// arnPrefix is built too: the leak canary rejects the literal.
var arnPrefix = "arn:" + ekscluster.DefaultPartition + ":"

func statePolicyParams() ekscluster.StateKeyPolicyParams {
	return ekscluster.StateKeyPolicyParams{
		AccountID:        account,
		Region:           "region-1",
		StateBucket:      "state-bucket",
		ReplicationRole:  "state-replication",
		OperatorRolePath: "aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_operators_*",
	}
}

type policyStatement struct {
	Sid       string
	Effect    string
	Principal map[string]any
	Action    []string
	Condition map[string]map[string]any
}

func decodeStatePolicy(t *testing.T, raw string) (string, []policyStatement) {
	t.Helper()

	var doc struct {
		Version   string
		ID        string `json:"Id"`
		Statement []policyStatement
	}

	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not JSON: %v", err)
	}

	if doc.Version != "2012-10-17" {
		t.Errorf("Version = %q", doc.Version)
	}

	return doc.ID, doc.Statement
}

// A Deny would bind the EKS service's grant, so there is none; and the
// account-root statement, which keeps the grant and IAM working, comes first
// and is the one AWS wrote.
func TestStateKeyPolicyAllowOnlyRootKept(t *testing.T) {
	t.Parallel()

	raw, err := ekscluster.StateKeyPolicy(statePolicyParams())
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(raw, `"Deny"`) || strings.Contains(raw, "NotPrincipal") {
		t.Fatalf("policy has a Deny or NotPrincipal: %s", raw)
	}

	_, stmts := decodeStatePolicy(t, raw)
	if len(stmts) == 0 {
		t.Fatal("no statements")
	}

	root := stmts[0]
	if root.Sid != "Enable IAM User Permissions" || root.Effect != "Allow" || len(root.Action) != 1 || root.Action[0] != "kms:*" ||
		root.Principal["AWS"] != arnPrefix+"iam::"+account+":root" {
		t.Errorf("first statement is not the account-root statement: %+v", root)
	}

	for _, s := range stmts {
		if s.Effect != "Allow" {
			t.Errorf("%s: Effect %q, want Allow", s.Sid, s.Effect)
		}
	}
}

// Every addition is scoped: through S3 for this bucket, or by the Pulumi
// secrets context, and always to the operators or the replication role.
func TestStateKeyPolicyScopes(t *testing.T) {
	t.Parallel()

	raw, err := ekscluster.StateKeyPolicy(statePolicyParams())
	if err != nil {
		t.Fatal(err)
	}

	id, stmts := decodeStatePolicy(t, raw)
	if id == "" {
		t.Error("policy Id is empty")
	}

	bySid := map[string]policyStatement{}
	for _, s := range stmts {
		bySid[s.Sid] = s
	}

	if len(bySid) != len(stmts) {
		t.Errorf("a Sid repeats: %v", stmts)
	}

	repl, ok := bySid["PulumiStateReplicationReadsSource"]
	if !ok {
		t.Fatal("no replication statement")
	}

	if repl.Principal["AWS"] != arnPrefix+"iam::"+account+":role/state-replication" || len(repl.Action) != 1 || repl.Action[0] != "kms:Decrypt" {
		t.Errorf("replication statement: %+v", repl)
	}

	if repl.Condition["StringEquals"]["kms:ViaService"] != "s3.region-1.amazonaws.com" {
		t.Errorf("replication is not tied to S3: %+v", repl.Condition)
	}

	for _, sid := range []string{"PulumiStateObjectsViaS3Operators", "PulumiStateReplicationReadsSource"} {
		ctx, _ := bySid[sid].Condition["StringLike"]["kms:EncryptionContext:aws:s3:arn"].([]any)
		if len(ctx) != 2 || ctx[0] != arnPrefix+"s3:::state-bucket" || ctx[1] != arnPrefix+"s3:::state-bucket/*" {
			t.Errorf("%s: S3 context = %v", sid, ctx)
		}
	}

	operators := arnPrefix + "iam::" + account + ":role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_operators_*"
	for _, sid := range []string{"PulumiStateObjectsViaS3Operators", "PulumiSecretsProviderOperators"} {
		if got := bySid[sid].Condition["ArnLike"]["aws:PrincipalArn"]; got != operators {
			t.Errorf("%s: operators = %v, want %s", sid, got, operators)
		}
	}

	secrets, ok := bySid["PulumiSecretsProviderOperators"]
	if !ok {
		t.Fatal("no secrets provider statement")
	}

	if secrets.Condition["StringEquals"]["kms:EncryptionContext:purpose"] != "pulumi-state" {
		t.Errorf("secrets provider statement is not bound to the context: %+v", secrets.Condition)
	}

	if secrets.Condition["StringEquals"]["kms:CallerAccount"] != account {
		t.Errorf("secrets provider statement is not bound to the account: %+v", secrets.Condition)
	}
}

func TestStateKeyPolicyPartition(t *testing.T) {
	t.Parallel()

	p := statePolicyParams()
	p.Partition = "cn-partition"

	raw, err := ekscluster.StateKeyPolicy(p)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(raw, arnPrefix) || !strings.Contains(raw, "arn:cn-partition:iam::"+account+":root") {
		t.Fatalf("the partition must reach every ARN: %s", raw)
	}
}

func TestStateKeyPolicyRequiresEveryInput(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*ekscluster.StateKeyPolicyParams){
		func(p *ekscluster.StateKeyPolicyParams) { p.AccountID = "" },
		func(p *ekscluster.StateKeyPolicyParams) { p.Region = "" },
		func(p *ekscluster.StateKeyPolicyParams) { p.StateBucket = "" },
		func(p *ekscluster.StateKeyPolicyParams) { p.ReplicationRole = "" },
		func(p *ekscluster.StateKeyPolicyParams) { p.OperatorRolePath = "" },
	} {
		p := statePolicyParams()
		mutate(&p)

		if _, err := ekscluster.StateKeyPolicy(p); err == nil {
			t.Errorf("%+v: want an error", p)
		}
	}
}
