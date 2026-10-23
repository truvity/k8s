package ekscluster

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	keyPolicyVersion = "2012-10-17"
	keyPolicyID      = "key-default-1"

	// Pulumi's awskms secrets provider is given this context
	// (context_purpose=pulumi-state in the stack's secretsprovider URL), so a
	// data key it wrapped cannot be unwrapped without it.
	stateContextKey   = "kms:EncryptionContext:purpose"
	stateContextValue = "pulumi-state"
	s3ContextKey      = "kms:EncryptionContext:aws:s3:arn"

	keyActionAll      = "kms:*"
	keyActionEncrypt  = "kms:Encrypt"
	keyActionDecrypt  = "kms:Decrypt"
	keyActionDataKeys = "kms:GenerateDataKey*"

	condStringEquals = "StringEquals"
	condStringLike   = "StringLike"
	condArnLike      = "ArnLike"
	condCallerAcct   = "kms:CallerAccount"
	condViaService   = "kms:ViaService"
	condPrincipalARN = "aws:PrincipalArn"
)

type (
	// StateKeyPolicyParams are the inputs of StateKeyPolicy: the account that
	// owns the key, the Pulumi state bucket the key also serves, and who may
	// operate on the state.
	StateKeyPolicyParams struct {
		// AccountID owns the key and the bucket.
		AccountID string
		// Region is the key's and the bucket's region.
		Region string
		// Partition is the AWS partition of the ARNs; empty is DefaultPartition ("aws").
		Partition string
		// StateBucket is the Pulumi state bucket that encrypts with the key.
		StateBucket string
		// ReplicationRole is the NAME of the role that replicates the bucket
		// and decrypts its objects at the source.
		ReplicationRole string
		// OperatorRolePath is the IAM role path-and-name pattern, inside
		// AccountID, of the principals that operate on the state (read its
		// objects through S3, and use the secrets provider): everything after
		// "role/" in the ARN, wildcards allowed. For an IAM Identity Center
		// permission set that is
		// "aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_<permission set>_*".
		OperatorRolePath string
	}

	keyPolicyDoc struct {
		Version   string
		ID        string `json:"Id"`
		Statement []keyPolicyStatement
	}

	keyPolicyStatement struct {
		Sid       string
		Effect    string
		Principal map[string]string
		Action    []string
		Resource  string
		Condition map[string]map[string]any `json:",omitempty"`
	}
)

// allow is an Allow statement for one AWS principal on every resource (the
// key itself).
func allow(sid, principal string, actions []string, condition map[string]map[string]any) keyPolicyStatement {
	return keyPolicyStatement{
		Sid:       sid,
		Effect:    "Allow",
		Principal: map[string]string{"AWS": principal},
		Action:    actions,
		Resource:  "*",
		Condition: condition,
	}
}

// StateKeyPolicy is the key policy of the cluster's secrets key (EksCluster.KeyARN) once the Pulumi state of the account
// encrypts with it too. Every addition is an Allow and the account-root
// statement stays as AWS wrote it (same Sid), which is what keeps the EKS
// service's grant, the cluster role and every IAM-allowed principal working; a
// Deny would bind the EKS grant as well, so there is none.
//
// The statements: the account root (kms:*); the operators decrypting the
// state's objects through S3 and only for this bucket; the replication role
// decrypting them at the source; and the operators using the awskms secrets
// provider, bound to its encryption context.
//
// KMS refuses a policy that names a principal that does not exist
// (MalformedPolicyDocumentException), so every principal named here must exist.
// The module's key carries no policy of its own: the caller attaches this one
// (a kms.KeyPolicy on the key's id) and so owns the document; destroying that
// resource puts AWS's default policy back.
func StateKeyPolicy(p StateKeyPolicyParams) (string, error) {
	if p.AccountID == "" || p.Region == "" || p.StateBucket == "" || p.ReplicationRole == "" || p.OperatorRolePath == "" {
		return "", fmt.Errorf(
			"state key policy: account, region, state bucket, replication role and operator role path are all required, got %+v", p)
	}

	partition := p.Partition
	if partition == "" {
		partition = DefaultPartition
	}

	account := p.AccountID
	iam := "arn:" + partition + ":iam::" + account + ":"
	viaS3 := "s3." + p.Region + ".amazonaws.com"
	// The context is the bucket's ARN with bucket keys on, the object's ARN
	// without; both are listed.
	bucketARN := "arn:" + partition + ":s3:::" + p.StateBucket
	s3Context := []string{bucketARN, bucketARN + "/*"}
	operators := iam + "role/" + strings.TrimPrefix(p.OperatorRolePath, "/")

	doc := keyPolicyDoc{
		Version: keyPolicyVersion,
		ID:      keyPolicyID,
		Statement: []keyPolicyStatement{
			allow("Enable IAM User Permissions", iam+"root", []string{keyActionAll}, nil),
			allow("PulumiStateObjectsViaS3Operators", "*", []string{keyActionDecrypt, keyActionDataKeys},
				map[string]map[string]any{
					condStringEquals: {condCallerAcct: account, condViaService: viaS3},
					condArnLike:      {condPrincipalARN: operators},
					condStringLike:   {s3ContextKey: s3Context},
				}),
			allow("PulumiStateReplicationReadsSource", iam+"role/"+p.ReplicationRole, []string{keyActionDecrypt},
				map[string]map[string]any{
					condStringEquals: {condViaService: viaS3},
					condStringLike:   {s3ContextKey: s3Context},
				}),
			allow("PulumiSecretsProviderOperators", "*", []string{keyActionEncrypt, keyActionDecrypt},
				map[string]map[string]any{
					condStringEquals: {condCallerAcct: account, stateContextKey: stateContextValue},
					condArnLike:      {condPrincipalARN: operators},
				}),
		},
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("state key policy: %w", err)
	}

	return string(out), nil
}
