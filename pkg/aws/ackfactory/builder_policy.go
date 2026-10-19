package ackfactory

import (
	"encoding/json"
	"fmt"
)

// tightening is the set of opt-in changes to a project builder's identity
// policy. The zero value changes nothing.
type tightening struct {
	workloadBoundary string
	protectTags      bool
	restrictKMS      bool
	dropSSM          bool
	dropDynamoDB     bool
	podIdentity      bool
}

func (t tightening) set() bool { return t != tightening{} }

// subject is what a builder's policy is keyed on: the project and the
// cluster. A project builder's policy names them (literals); an employee
// builder's policy reads them from the role's own tags, so one document
// serves every sandbox.
type subject struct {
	project, cluster string
}

func literalSubject(c Cluster, project string) subject {
	return subject{project: project, cluster: c.Name}
}

// ownRole is the builder's own role ARN.
func (s subject) ownRole(c Cluster) string {
	return c.roleARN(s.cluster + "-ack-project-" + s.project)
}

// stmt builds one policy statement.
func stmt(sid, effect string, actions []string, resource any, cond map[string]any) map[string]any {
	m := map[string]any{
		"Sid":      sid,
		"Effect":   effect,
		"Action":   actions,
		"Resource": resource,
	}

	if cond != nil {
		m["Condition"] = cond
	}

	return m
}

// Tag-writing and tag-removing actions of the services a builder uses.
var (
	tagWriteActions = []string{
		"iam:CreateRole", "iam:CreatePolicy", "iam:TagRole", "iam:TagPolicy",
		"kms:CreateKey", "kms:TagResource",
		"eks:CreatePodIdentityAssociation", "eks:TagResource",
	}
	tagRemoveActions = []string{
		"iam:UntagRole", "iam:UntagPolicy", "kms:UntagResource", "eks:UntagResource",
	}
	foreignTagActions = []string{
		"iam:TagRole", "iam:UntagRole", "eks:TagResource", "eks:UntagResource",
	}
	ownRoleActions = []string{
		"iam:TagRole", "iam:UntagRole", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
		"iam:AttachRolePolicy", "iam:DetachRolePolicy", "iam:UpdateAssumeRolePolicy",
		"iam:PutRolePermissionsBoundary", "iam:DeleteRolePermissionsBoundary",
		"iam:UpdateRole", "iam:UpdateRoleDescription", "iam:DeleteRole",
	}
	// What decides who owns a key and when it dies.
	keyRetirementActions = []string{
		"kms:PutKeyPolicy", "kms:CreateGrant", "kms:RetireGrant", "kms:RevokeGrant",
		"kms:ScheduleKeyDeletion", "kms:DisableKey",
	}
	// What a project does to its own keys once it has them: no key policy,
	// grants or retirement.
	keyManageActions = []string{
		"kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus",
		"kms:ListResourceTags", "kms:ListKeyPolicies", "kms:ListGrants",
		"kms:TagResource", "kms:UntagResource", "kms:UpdateKeyDescription",
		"kms:EnableKey", "kms:EnableKeyRotation", "kms:DisableKeyRotation",
		"kms:CreateAlias", "kms:UpdateAlias", "kms:DeleteAlias",
	}
	podIdentityActions = []string{
		"eks:CreatePodIdentityAssociation", "eks:DeletePodIdentityAssociation",
		"eks:DescribePodIdentityAssociation", "eks:UpdatePodIdentityAssociation",
		"eks:ListPodIdentityAssociations", "eks:TagResource", "eks:UntagResource",
	}
)

// tagGuards deny changing or removing the project and cluster tags. The role
// boundaries and the workload roles are keyed on them.
func tagGuards(c Cluster, s subject) []map[string]any {
	retag := func(sid, key, want string) map[string]any {
		return stmt(sid, "Deny", tagWriteActions, "*", map[string]any{
			"Null":            map[string]any{"aws:RequestTag/" + key: "false"},
			"StringNotEquals": map[string]any{"aws:RequestTag/" + key: want},
		})
	}

	return []map[string]any{
		retag("DenyRetagProject", "project", s.project),
		retag("DenyRetagCluster", "cluster", s.cluster),
		stmt("DenyUntagProjectOrCluster", "Deny", tagRemoveActions, "*", map[string]any{
			"ForAnyValue:StringEquals": map[string]any{"aws:TagKeys": []string{"project", "cluster"}},
		}),
		stmt("DenyRetagForeignResources", "Deny", foreignTagActions, "*", map[string]any{
			"Null":            map[string]any{"aws:ResourceTag/project": "false"},
			"StringNotEquals": map[string]any{"aws:ResourceTag/project": s.project},
		}),
		stmt("DenyTouchOwnRole", "Deny", ownRoleActions, s.ownRole(c), nil),
	}
}

func kmsManage(s subject) map[string]any {
	return stmt("KMSManageByProjectTag", "Allow", keyManageActions, "*", map[string]any{
		"StringEquals": map[string]any{"aws:ResourceTag/project": s.project},
	})
}

func kmsDenyRetirement() map[string]any {
	return stmt("DenyKeyPolicyGrantsAndRetirement", "Deny", keyRetirementActions, "*", nil)
}

// podIdentity is what a builder needs to be the EKS controller's identity for
// its project: the association calls on its cluster, and PassRole of its roles
// to Pod Identity.
func podIdentity(c Cluster, s subject, roles []string) []map[string]any {
	return []map[string]any{
		stmt("AllowPodIdentityAssociations", "Allow", podIdentityActions, []string{
			c.ARN(),
			fmt.Sprintf("arn:%s:eks:%s:%s:podidentityassociation/%s/*", c.partition(), c.Region, c.AccountID, s.cluster),
		}, nil),
		stmt("AllowPassOwnRoleToPodIdentity", "Allow", []string{"iam:PassRole"}, roles, map[string]any{
			"StringEqualsIfExists": map[string]any{"iam:PassedToService": "pods.eks.amazonaws.com"},
		}),
	}
}

// onlyWorkloadBoundary denies creating a role, or changing a role's boundary,
// to anything but the workload boundary (or to none).
func onlyWorkloadBoundary(c Cluster, boundary string) map[string]any {
	return stmt("DenyOtherBoundary", "Deny", []string{"iam:CreateRole", "iam:PutRolePermissionsBoundary"}, "*", map[string]any{
		"StringNotEquals": map[string]any{"iam:PermissionsBoundary": c.policyARN(boundary)},
	})
}

// projectPolicy is a project role's identity policy.
func projectPolicy(c Cluster, name, ssmPrefix string, forbidden []string, t tightening) string {
	doc := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "IAMRead",
    "Effect": "Allow",
    "Action": ["iam:Get*", "iam:List*"],
    "Resource": "*"
  },{
    "Sid": "IAMWriteProjectPrefixed",
    "Effect": "Allow",
    "Action": "iam:*",
    "Resource": [
      "arn:%[4]s:iam::%[1]s:role/*%[2]s*",
      "arn:%[4]s:iam::%[1]s:policy/*%[2]s*",
      "arn:%[4]s:iam::%[1]s:instance-profile/*%[2]s*"
    ]
  },{
    "Sid": "S3AccountReads",
    "Effect": "Allow",
    "Action": ["s3:ListAllMyBuckets", "s3:GetBucketLocation"],
    "Resource": "*"
  },{
    "Sid": "S3ProjectPrefixed",
    "Effect": "Allow",
    "Action": "s3:*",
    "Resource": ["arn:%[4]s:s3:::*%[2]s*", "arn:%[4]s:s3:::*%[2]s*/*"]
  },{
    "Sid": "KMSByProjectTag",
    "Effect": "Allow",
    "Action": "kms:*",
    "Resource": "*",
    "Condition": {"StringEquals": {"aws:ResourceTag/project": "%[2]s"}}
  },{
    "Sid": "KMSCreateKeyWithProjectTag",
    "Effect": "Allow",
    "Action": ["kms:CreateKey", "kms:TagResource"],
    "Resource": "*",
    "Condition": {"StringEquals": {"aws:RequestTag/project": "%[2]s"}}
  },{
    "Sid": "DenyRetagForeignKeys",
    "Effect": "Deny",
    "Action": ["kms:TagResource", "kms:UntagResource"],
    "Resource": "*",
    "Condition": {"Null": {"aws:ResourceTag/project": "false"}, "StringNotEquals": {"aws:ResourceTag/project": "%[2]s"}}
  },{
    "Sid": "KMSAliasList",
    "Effect": "Allow",
    "Action": "kms:ListAliases",
    "Resource": "*"
  },{
    "Sid": "KMSAliasByProjectName",
    "Effect": "Allow",
    "Action": ["kms:CreateAlias", "kms:UpdateAlias", "kms:DeleteAlias"],
    "Resource": "arn:%[4]s:kms:*:%[1]s:alias/*-%[2]s-*"
  },{
    "Sid": "DynamoDBAccountReads",
    "Effect": "Allow",
    "Action": ["dynamodb:ListTables", "dynamodb:DescribeLimits"],
    "Resource": "*"
  },{
    "Sid": "DynamoDBProjectPrefixed",
    "Effect": "Allow",
    "Action": "dynamodb:*",
    "Resource": [
      "arn:%[4]s:dynamodb:*:%[1]s:table/*%[2]s*",
      "arn:%[4]s:dynamodb:*:%[1]s:table/*%[2]s*/*"
    ]
  },{
    "Sid": "SSMByProjectPrefix",
    "Effect": "Allow",
    "Action": "ssm:*",
    "Resource": "arn:%[4]s:ssm:*:%[1]s:parameter%[3]s/*"
  }]
}`, c.AccountID, name, ssmPrefix, c.partition())

	if !t.set() && len(forbidden) == 0 {
		return doc
	}

	var policy struct {
		Version   string
		Statement []map[string]any
	}

	if err := json.Unmarshal([]byte(doc), &policy); err != nil {
		panic(fmt.Sprintf("ackfactory: project policy is not JSON: %v", err))
	}

	s := literalSubject(c, name)
	drop := map[string]bool{}

	if t.dropSSM {
		drop["SSMByProjectPrefix"] = true
	}

	if t.dropDynamoDB {
		drop["DynamoDBAccountReads"] = true
		drop["DynamoDBProjectPrefixed"] = true
	}

	var out []map[string]any

	for _, st := range policy.Statement {
		sid, _ := st["Sid"].(string)

		switch {
		case drop[sid]:
			continue
		case t.restrictKMS && sid == "KMSByProjectTag":
			st = kmsManage(s)
		case t.restrictKMS && sid == "KMSAliasList":
			st["Action"] = []string{"kms:ListAliases", "kms:ListKeys"}
		}

		out = append(out, st)
	}

	if t.protectTags {
		out = append(out, tagGuards(c, s)...)
	}

	if t.restrictKMS {
		out = append(out, kmsDenyRetirement())
	}

	if t.podIdentity {
		out = append(out, podIdentity(c, s, []string{c.roleARN("*" + name + "*")})...)
	}

	if t.workloadBoundary != "" {
		out = append(out, onlyWorkloadBoundary(c, t.workloadBoundary))
	}

	if len(forbidden) > 0 {
		resources := make([]string, 0, len(forbidden))
		for _, pattern := range forbidden {
			resources = append(resources, c.roleARN(pattern))
		}

		out = append(out, map[string]any{
			"Sid":      "DenyPlatformMintedRoles",
			"Effect":   "Deny",
			"Action":   "iam:*",
			"Resource": resources,
		})
	}

	return marshalPolicy(policy.Version, out)
}

func marshalPolicy(version string, statements []map[string]any) string {
	b, err := json.MarshalIndent(map[string]any{"Version": version, "Statement": statements}, "", "  ")
	if err != nil {
		panic(err)
	}

	return string(b)
}
