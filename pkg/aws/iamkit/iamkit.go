// Package iamkit registers loose IAM policies, roles and attachments with one
// provider, one permissions boundary and one logical-name prefix, and names
// the IAM policy language so documents read as policy rather than strings.
//
// It is for a program that owns many small identities and builds their
// documents itself; an identity with a fixed shape (an EKS Pod Identity role,
// say) belongs in its own component (pkg/aws/podidentity). Nothing here is a
// ComponentResource: every resource is registered where the caller's context
// puts it, under the name Kit.Prefix + name, so a program that registered
// them by hand adopts the kit with no change of URN.
//
// Nothing here imports. A standing import declaration makes a resource
// impossible to replace (the first change of an immutable field fails the
// apply), and iam.Policy's description is immutable: adopt a live object with
// a one-time `pulumi import`, then declare it plainly.
package iamkit

import (
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The IAM policy language: document keys and the values they take.
const (
	Version   = "Version"
	Statement = "Statement"
	Sid       = "Sid"
	Effect    = "Effect"
	Action    = "Action"
	Resource  = "Resource"
	Principal = "Principal"
	Condition = "Condition"
	// Service is the Principal key of an AWS service principal.
	Service = "Service"

	// DocVersion is the current policy language version, Version's value.
	DocVersion = "2012-10-17"

	Allow = "Allow"
	Deny  = "Deny"

	// Condition operators.
	StringEquals            = "StringEquals"
	StringLike              = "StringLike"
	ArnEquals               = "ArnEquals"
	ForAnyValueStringEquals = "ForAnyValue:StringEquals"

	// STS actions of a trust policy.
	AssumeRole                = "sts:AssumeRole"
	TagSession                = "sts:TagSession"
	AssumeRoleWithWebIdentity = "sts:AssumeRoleWithWebIdentity"
)

// Kit registers IAM objects in one context with one provider.
type Kit struct {
	// Ctx is the program's context. Required.
	Ctx *pulumi.Context
	// Provider is the AWS provider of the objects' account. Required: the
	// kit never falls back to a default provider.
	Provider pulumi.ProviderResource
	// Prefix starts every logical name ("ids/" names a policy "ids/<name>").
	// It is part of the URN: changing it renames every object.
	Prefix string
	// Boundary is the ARN of every role's permissions boundary. Empty sets
	// none.
	Boundary string
	// Opts are added to every registration (a parent, say).
	Opts []pulumi.ResourceOption
}

func (k Kit) opts() []pulumi.ResourceOption {
	return append([]pulumi.ResourceOption{pulumi.Provider(k.Provider)}, k.Opts...)
}

func (k Kit) roleArgs(name string, trust pulumi.StringInput) *iam.RoleArgs {
	args := &iam.RoleArgs{
		Name:             pulumi.String(name),
		AssumeRolePolicy: trust,
	}
	if k.Boundary != "" {
		args.PermissionsBoundary = pulumi.String(k.Boundary)
	}

	return args
}

// Policy registers a customer-managed policy named name, logical name
// Prefix+name.
//
// An IAM policy's description is immutable: another description replaces the
// policy, which detaches it from its roles for a window. Do not edit one on
// its own.
func (k Kit) Policy(name, description, document string) (*iam.Policy, error) {
	return iam.NewPolicy(k.Ctx, k.Prefix+name, &iam.PolicyArgs{
		Name:        pulumi.String(name),
		Description: pulumi.String(description),
		Policy:      pulumi.String(document),
	}, k.opts()...)
}

// Role registers a role named name, trusting trust, with policy attached:
// the role is logical name Prefix+name, the attachment Prefix+name+
// "/attachment". trust may be an output (a principal's ARN).
func (k Kit) Role(name string, trust pulumi.StringInput, policy *iam.Policy) (*iam.Role, error) {
	role, err := k.BareRole(name, trust)
	if err != nil {
		return nil, err
	}

	if _, err := iam.NewRolePolicyAttachment(k.Ctx, k.Prefix+name+"/attachment", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: policy.Arn,
	}, k.opts()...); err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}

	return role, nil
}

// BareRole registers a role with no policy of its own, logical name
// Prefix+name: its permissions all arrive through Attach. A role that should
// carry one policy is Role; a nil policy is not a way to ask for this one.
func (k Kit) BareRole(name string, trust pulumi.StringInput) (*iam.Role, error) {
	return iam.NewRole(k.Ctx, k.Prefix+name, k.roleArgs(name, trust), k.opts()...)
}

// Attach binds a shared managed policy to a role known by its name, logical
// name Prefix+label. The policy is the grant, the attachment the decision: a
// policy defined once and attached to many roles cannot drift between them.
// label is part of the URN and must be stable; "<role>/<policy>" is the
// convention.
func (k Kit) Attach(label string, roleName pulumi.StringInput, policy *iam.Policy) error {
	if _, err := iam.NewRolePolicyAttachment(k.Ctx, k.Prefix+label, &iam.RolePolicyAttachmentArgs{
		Role:      roleName,
		PolicyArn: policy.Arn,
	}, k.opts()...); err != nil {
		return fmt.Errorf("attach %s: %w", label, err)
	}

	return nil
}

// JSON renders a policy document.
func JSON(doc any) (string, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render policy document: %w", err)
	}

	return string(raw), nil
}

// MustJSON is JSON for a document built from literals, which always
// renders: it panics otherwise.
func MustJSON(doc any) string {
	out, err := JSON(doc)
	if err != nil {
		panic(err)
	}

	return out
}
