package ackfactory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// DefaultEmployeePrefix is the name prefix of an employee sandbox project
// when EmployeeRolesOptions.NamePrefix is empty.
const DefaultEmployeePrefix = "emp-"

var sandboxName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// EmployeeSandbox is one employee's sandbox project.
type EmployeeSandbox struct {
	// Name is the sandbox's project name: the builder is
	// <cluster>-ack-project-<Name> and carries the tags project=<Name> and
	// cluster=<cluster>. It must start with the prefix (default "emp-") and be
	// lower-case letters, digits and single hyphens. Required.
	Name string
}

// EmployeeRolesOptions are the inputs of EmployeeRoles.
type EmployeeRolesOptions struct {
	// Boundary is the NAME of the permissions boundary of the builder roles
	// themselves, the same one the project builders carry. Required.
	Boundary string
	// WorkloadBoundary is the NAME of the one permissions boundary the
	// sandbox may put on the roles it creates: the project workload boundary,
	// which holds data actions and no IAM. The identity policy refuses every
	// other boundary and none. Required.
	WorkloadBoundary string
	// NamePrefix is the prefix every sandbox name must carry. Empty means
	// DefaultEmployeePrefix.
	NamePrefix string
	// BucketPrefix is what comes before <cluster>-<project>- in the names of
	// the buckets a sandbox may administer, for example "acme-". May be
	// empty.
	BucketPrefix string
	// Controllers are the short names of the controllers allowed to assume
	// the roles. Nil means DefaultProjectControllers.
	Controllers []string
	// ResourceOptions apply to every resource, normally the AWS provider.
	ResourceOptions []pulumi.ResourceOption
}

// EmployeeRoles mints one {cluster}-ack-project-{name} builder per employee
// sandbox, for namespaces whose ACK resources are not a project's: the
// controllers assume it for the CRs of the sandbox's namespace, as they do a
// project's.
//
// It mirrors ProjectRoles with every tightening on, and is keyed on the role's
// own tags (project and cluster) instead of a name written into the policy, so
// one document serves every sandbox: roles and policies are named
// {cluster}-{project}.*, buckets {BucketPrefix}{cluster}-{project}-*, KMS
// aliases alias/{cluster}-{project}-*. The sandbox may create roles only under
// the workload boundary, never reads object data, SSM or DynamoDB, and cannot
// change its project and cluster tags, key policies or grants, nor retire a
// key. It is the Pod Identity identity of the EKS controller too (add "eks" to
// Controllers for that).
func EmployeeRoles(ctx *pulumi.Context, c Cluster, o EmployeeRolesOptions, sandboxes []EmployeeSandbox) error {
	if err := c.Validate(); err != nil {
		return err
	}

	var errs []error

	if o.Boundary == "" {
		errs = append(errs, errors.New("options: Boundary is empty"))
	}

	if o.WorkloadBoundary == "" {
		errs = append(errs, errors.New("options: WorkloadBoundary is empty"))
	}

	if o.WorkloadBoundary != "" && o.WorkloadBoundary == o.Boundary {
		errs = append(errs, errors.New("options: WorkloadBoundary equals Boundary"))
	}

	prefix := o.NamePrefix
	if prefix == "" {
		prefix = DefaultEmployeePrefix
	}

	for _, sb := range sandboxes {
		if !strings.HasPrefix(sb.Name, prefix) || len(sb.Name) == len(prefix) || !sandboxName.MatchString(sb.Name) {
			errs = append(errs, fmt.Errorf("sandbox %q: the name must start with %q and be lower-case letters, digits and single hyphens", sb.Name, prefix))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return err
	}

	policy := employeePolicy(c, o)

	for _, sb := range sandboxes {
		if err := newBuilderRole(ctx, c, o.Boundary, sb.Name, o.Controllers, policy, o.ResourceOptions); err != nil {
			return fmt.Errorf("employee role for %s: %w", sb.Name, err)
		}
	}

	return nil
}

// employeePolicy is the identity policy of every employee builder: the project
// builder's, tightened, with the project and cluster read from the role's tags.
func employeePolicy(c Cluster, o EmployeeRolesOptions) string {
	s := taggedSubject()
	roles := c.roleARN(s.cluster + "-" + s.project + ".*")
	policies := c.iamARN("policy/" + s.cluster + "-" + s.project + ".*")
	buckets := fmt.Sprintf("arn:%s:s3:::%s%s-%s-*", c.partition(), o.BucketPrefix, s.cluster, s.project)
	ownProject := map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/project": s.project}}
	boundary := c.policyARN(o.WorkloadBoundary)

	st := []map[string]any{
		stmt("AllowIAMRead", "Allow", []string{"iam:Get*", "iam:List*"}, "*", nil),
		stmt("AllowCreateWorkloadRole", "Allow", []string{"iam:CreateRole"}, roles, map[string]any{
			"StringEquals": map[string]any{
				"aws:RequestTag/project":  s.project,
				"aws:RequestTag/cluster":  s.cluster,
				"iam:PermissionsBoundary": boundary,
			},
		}),
		stmt("AllowSetWorkloadBoundary", "Allow", []string{"iam:PutRolePermissionsBoundary"}, roles, map[string]any{
			"StringEquals": map[string]any{
				"aws:ResourceTag/project": s.project,
				"iam:PermissionsBoundary": boundary,
			},
		}),
		stmt("AllowManageOwnRole", "Allow", []string{
			"iam:UpdateRole", "iam:UpdateRoleDescription", "iam:UpdateAssumeRolePolicy", "iam:DeleteRole",
			"iam:TagRole", "iam:UntagRole", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
		}, roles, ownProject),
		stmt("AllowAttachOwnPolicy", "Allow", []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"}, roles, map[string]any{
			"StringEquals": map[string]any{"aws:ResourceTag/project": s.project},
			"ArnLike":      map[string]any{"iam:PolicyARN": policies},
		}),
		stmt("AllowManageOwnPolicy", "Allow", []string{
			"iam:CreatePolicy", "iam:CreatePolicyVersion", "iam:DeletePolicy", "iam:DeletePolicyVersion",
			"iam:SetDefaultPolicyVersion", "iam:TagPolicy", "iam:UntagPolicy",
		}, policies, nil),
	}

	st = append(st, podIdentity(c, s, []string{roles})...)
	st = append(st,
		stmt("AllowOwnBucketAdmin", "Allow", []string{
			"s3:CreateBucket", "s3:ListBucket", "s3:GetBucket*", "s3:PutBucket*", "s3:DeleteBucket*",
			"s3:Get*Configuration", "s3:Put*Configuration",
		}, buckets, nil),
		stmt("AllowBucketList", "Allow", []string{"s3:ListAllMyBuckets"}, "*", nil),
		kmsCreate(s),
		kmsManage(s),
		stmt("AllowOwnAlias", "Allow", []string{"kms:CreateAlias", "kms:UpdateAlias", "kms:DeleteAlias"},
			fmt.Sprintf("arn:%s:kms:*:%s:alias/%s-%s-*", c.partition(), c.AccountID, s.cluster, s.project), nil),
		stmt("AllowKeyList", "Allow", []string{"kms:ListAliases", "kms:ListKeys"}, "*", nil),
		kmsNoTakeover(s),
		kmsDenyRetirement(),
		onlyWorkloadBoundary(c, o.WorkloadBoundary),
	)
	st = append(st, tagGuards(c, s)...)

	return marshalPolicy("2012-10-17", st)
}

const (
	principalProject = "${aws:PrincipalTag/project}"
	principalCluster = "${aws:PrincipalTag/cluster}"
)

func taggedSubject() subject {
	return subject{project: principalProject, cluster: principalCluster}
}

// kmsCreate lets the builder create a key carrying its own project tag.
func kmsCreate(s subject) map[string]any {
	return stmt("KMSCreateKeyWithProjectTag", "Allow", []string{"kms:CreateKey", "kms:TagResource"}, "*", map[string]any{
		"StringEquals": map[string]any{"aws:RequestTag/project": s.project},
	})
}

// kmsNoTakeover denies tagging or untagging a key that already carries
// another project's tag: with kmsCreate's tagging grant it would otherwise
// be a way to claim that key.
func kmsNoTakeover(s subject) map[string]any {
	return stmt("DenyRetagForeignKeys", "Deny", []string{"kms:TagResource", "kms:UntagResource"}, "*", map[string]any{
		"Null":            map[string]any{"aws:ResourceTag/project": "false"},
		"StringNotEquals": map[string]any{"aws:ResourceTag/project": s.project},
	})
}
