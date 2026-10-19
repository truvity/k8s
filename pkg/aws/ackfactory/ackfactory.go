package ackfactory

import (
	"context"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/podidentity"
)

const (
	actionAssumeRole = "sts:AssumeRole"
	actionTagSession = "sts:TagSession"

	// DefaultPartition is the ARN partition used when Cluster.Partition is empty.
	DefaultPartition = "aws"
)

// Cluster is the EKS cluster the controllers run in.
type Cluster struct {
	// Name is the cluster's name. Every role is named after it. Required.
	Name string
	// AccountID is the AWS account of the cluster. Required.
	AccountID string
	// Region is the cluster's region. Required.
	Region string
	// Partition is the ARN partition. Empty means DefaultPartition.
	Partition string
}

func (c Cluster) partition() string {
	if c.Partition == "" {
		return DefaultPartition
	}

	return c.Partition
}

// ARN is the cluster's ARN: the aws:SourceArn EKS stamps on every Pod
// Identity assume, which the trust policies pin.
func (c Cluster) ARN() string {
	return fmt.Sprintf("arn:%s:eks:%s:%s:cluster/%s", c.partition(), c.Region, c.AccountID, c.Name)
}

func (c Cluster) policyARN(name string) string {
	return fmt.Sprintf("arn:%s:iam::%s:policy/%s", c.partition(), c.AccountID, name)
}

func (c Cluster) roleARN(name string) string {
	return fmt.Sprintf("arn:%s:iam::%s:role/%s", c.partition(), c.AccountID, name)
}

func (c Cluster) iamARN(suffix string) string {
	return fmt.Sprintf("arn:%s:iam::%s:%s", c.partition(), c.AccountID, suffix)
}

// Validate reports every problem with the cluster at once, or returns nil.
func (c Cluster) Validate() error {
	var errs []error

	if c.Name == "" {
		errs = append(errs, errors.New("cluster: Name is empty"))
	}

	if c.AccountID == "" {
		errs = append(errs, errors.New("cluster: AccountID is empty"))
	}

	if c.Region == "" {
		errs = append(errs, errors.New("cluster: Region is empty"))
	}

	return errors.Join(errs...)
}

// Options are the inputs every function that registers a Pod Identity role
// shares.
type Options struct {
	// Provider is the AWS provider of the cluster's account and region.
	// Required: nothing here falls back to a default provider.
	Provider *aws.Provider
	// ClusterName is the cluster the associations are in. Pass the cluster's
	// Name output to order the associations after the cluster. Required.
	ClusterName pulumi.StringInput
	// Boundary is the NAME (not the ARN) of the permissions boundary the
	// controller roles carry. Required.
	Boundary string
}

func (o Options) validate(c Cluster) error {
	var errs []error

	if err := c.Validate(); err != nil {
		errs = append(errs, err)
	}

	if o.Provider == nil {
		errs = append(errs, errors.New("options: Provider is nil"))
	}

	if o.ClusterName == nil {
		errs = append(errs, errors.New("options: ClusterName is nil"))
	}

	if o.Boundary == "" {
		errs = append(errs, errors.New("options: Boundary is empty"))
	}

	return errors.Join(errs...)
}

// policyJSON renders a policy document through the provider's invoke, so the
// document is validated and ordered as the provider orders it.
func policyJSON(ctx *pulumi.Context, provider *aws.Provider, statements []iam.GetPolicyDocumentStatement) (string, error) {
	doc, err := iam.GetPolicyDocument(ctx, &iam.GetPolicyDocumentArgs{
		Statements: statements,
	}, pulumi.Provider(provider))
	if err != nil {
		return "", fmt.Errorf("get policy document: %w", err)
	}

	return doc.Json, nil
}

// IAM deploys the {cluster}-ack-iam role and its association: the
// controller that mints every other IAM role in the bundle.
//
// WithProjectAssume additionally lets the controller assume the per-project
// capability roles ({cluster}-ack-project-*): set it on clusters that host
// projects.
//
// The trust is podidentity's default: the cluster's account and ARN, and the
// controller's namespace and service account (ack-iam/ack-iam-controller),
// the same pair the association binds. Without the last two, any Pod Identity
// association in the cluster could name this role.
//
// It returns the identity; the caller exports the role ARN if it wants to.
func IAM(ctx *pulumi.Context, c Cluster, o Options, withProjectAssume bool) (*podidentity.PodIdentity, error) {
	if err := o.validate(c); err != nil {
		return nil, err
	}

	statements := []iam.GetPolicyDocumentStatement{
		{
			Sid:       pulumi.StringRef("IAMManagement"),
			Effect:    pulumi.StringRef("Allow"),
			Actions:   []string{"iam:*"},
			Resources: []string{c.iamARN("*")},
		},
		{
			Sid:    pulumi.StringRef("EKSPodIdentity"),
			Effect: pulumi.StringRef("Allow"),
			Actions: []string{
				"eks:CreatePodIdentityAssociation",
				"eks:DeletePodIdentityAssociation",
				"eks:DescribePodIdentityAssociation",
				"eks:ListPodIdentityAssociations",
			},
			Resources: []string{"*"},
		},
	}

	if withProjectAssume {
		// TagSession: Pod Identity sessions carry transitive session tags, so
		// chaining into the project roles needs it.
		statements = append(statements, iam.GetPolicyDocumentStatement{
			Sid:       pulumi.StringRef("AssumeProjectRoles"),
			Effect:    pulumi.StringRef("Allow"),
			Actions:   []string{actionAssumeRole, actionTagSession},
			Resources: []string{c.roleARN(c.Name + "-ack-project-*")},
		})
	}

	policy, err := policyJSON(ctx, o.Provider, statements)
	if err != nil {
		return nil, fmt.Errorf("build ACK IAM inline policy: %w", err)
	}

	role, err := podidentity.New(ctx, "ack-iam-"+c.Name, &podidentity.Args{
		Provider:            o.Provider,
		ClusterName:         o.ClusterName,
		Namespace:           "ack-iam",
		ServiceAccounts:     []string{"ack-iam-controller"},
		RoleName:            c.Name + "-ack-iam",
		PermissionsBoundary: c.policyARN(o.Boundary),
		AccountID:           c.AccountID,
		ClusterARN:          c.ARN(),
		InlinePolicy:        &podidentity.InlinePolicy{Name: c.Name + "-ack-iam-policy", Document: pulumi.String(policy)},
		LegacyTopLevel:      true,
		Names: func(k podidentity.Child) string {
			switch k.Kind {
			case podidentity.KindRolePolicy:
				return c.Name + "-ack-iam-policy"
			case podidentity.KindAssociation:
				return "ack-iam-" + c.Name + "-assoc"
			default:
				return c.Name + "-ack-iam-role"
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create ACK IAM identity: %w", err)
	}

	return role, nil
}

// EKS deploys the {cluster}-ack-eks role and its association: the controller
// that mints Pod Identity associations for workloads. Its permissions are the
// controller's recommended inline policy, with the IAM reads and PassRole
// scoped to roles named after the cluster.
//
// The trust is pinned like IAM's, to ack-eks/ack-eks-controller.
//
// It returns the identity; the caller exports the role ARN if it wants to.
func EKS(ctx *pulumi.Context, c Cluster, o Options) (*podidentity.PodIdentity, error) {
	if err := o.validate(c); err != nil {
		return nil, err
	}

	policy, err := policyJSON(ctx, o.Provider, []iam.GetPolicyDocumentStatement{
		{
			Sid:    pulumi.StringRef("EKSPodIdentityManagement"),
			Effect: pulumi.StringRef("Allow"),
			Actions: []string{
				"eks:CreatePodIdentityAssociation",
				"eks:DeletePodIdentityAssociation",
				"eks:DescribePodIdentityAssociation",
				"eks:UpdatePodIdentityAssociation",
				"eks:ListPodIdentityAssociations",
				"eks:TagResource",
				"eks:UntagResource",
			},
			Resources: []string{"*"},
		},
		{
			Sid:    pulumi.StringRef("IAMForPodIdentity"),
			Effect: pulumi.StringRef("Allow"),
			Actions: []string{
				"iam:GetRole",
				"iam:PassRole",
				"iam:ListAttachedRolePolicies",
			},
			Resources: []string{c.roleARN(c.Name + "-*")},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build ACK EKS inline policy: %w", err)
	}

	role, err := podidentity.New(ctx, "ack-eks-"+c.Name, &podidentity.Args{
		Provider:            o.Provider,
		ClusterName:         o.ClusterName,
		Namespace:           "ack-eks",
		ServiceAccounts:     []string{"ack-eks-controller"},
		RoleName:            c.Name + "-ack-eks",
		PermissionsBoundary: c.policyARN(o.Boundary),
		AccountID:           c.AccountID,
		ClusterARN:          c.ARN(),
		InlinePolicy:        &podidentity.InlinePolicy{Name: c.Name + "-ack-eks-policy", Document: pulumi.String(policy)},
		LegacyTopLevel:      true,
		Names: func(k podidentity.Child) string {
			switch k.Kind {
			case podidentity.KindRolePolicy:
				return c.Name + "-ack-eks-policy"
			case podidentity.KindAssociation:
				return "ack-eks-" + c.Name + "-assoc"
			default:
				return c.Name + "-ack-eks-role"
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create ACK EKS identity: %w", err)
	}

	return role, nil
}

// Lookup answers what already exists, so a live role, policy or association
// is adopted instead of created. The answers must be known when the program
// declares the resource, which is why they are lookups and not outputs.
type Lookup interface {
	// PolicyExists says whether the managed policy with this ARN exists.
	PolicyExists(ctx context.Context, arn string) (bool, error)
	// RoleExists says whether the role with this name exists.
	RoleExists(ctx context.Context, name string) (bool, error)
	// AssociationID returns the ID of the Pod Identity association of the
	// service account, or "" when there is none. A lookup that fails must
	// return an error: a failure silently read as "none" would turn an
	// adoption into a colliding create.
	AssociationID(ctx context.Context, cluster, namespace, serviceAccount string) (string, error)
}

// Service is one service controller's identity shape.
type Service struct {
	// Name is the short name (s3, kms, dynamodb): the namespace is
	// ack-<Name>, the service account ack-<Name>-controller, and the role and
	// the managed policy are both <cluster>-ack-<Name>.
	Name string
	// Description is the managed policy's description. IAM cannot change it
	// once set, so adopting an existing policy needs the live value.
	Description string
	// Action is the wildcard grant, for example "s3:*".
	Action string
}

// DefaultServices is the controller set the factory was built for.
func DefaultServices() []Service {
	return []Service{
		{Name: "s3", Description: "S3 full access for the ACK S3 controller", Action: "s3:*"},
		{Name: "kms", Description: "KMS full access for the ACK KMS controller", Action: "kms:*"},
		{Name: "dynamodb", Description: "DynamoDB full access for the ACK DynamoDB controller", Action: "dynamodb:*"},
	}
}

// ServicesOptions are the inputs of Services.
type ServicesOptions struct {
	Options
	// Lookup adopts what is live. Required.
	Lookup Lookup
	// Services is the controller set. Nil means DefaultServices.
	Services []Service
}

// Services deploys, or adopts, the identity of each service controller: a
// managed policy, a role under the boundary, the attachment and the
// association, looked up with the program's context. Each controller may also assume the project roles
// ({cluster}-ack-project-*).
func Services(ctx *pulumi.Context, c Cluster, o ServicesOptions) error {
	if err := o.validate(c); err != nil {
		return err
	}

	if o.Lookup == nil {
		return errors.New("options: Lookup is nil")
	}

	svcs := o.Services
	if svcs == nil {
		svcs = DefaultServices()
	}

	for _, svc := range svcs {
		if err := deployService(ctx, c, o, svc); err != nil {
			return fmt.Errorf("ack-%s identity: %w", svc.Name, err)
		}
	}

	return nil
}

func deployService(ctx *pulumi.Context, c Cluster, o ServicesOptions, svc Service) error {
	name := fmt.Sprintf("%s-ack-%s", c.Name, svc.Name)
	ns := "ack-" + svc.Name
	sa := ns + "-controller"
	policyARN := c.policyARN(name)

	policyDoc := fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":%q,"Resource":"*"},`+
			`{"Sid":"AssumeProjectRoles","Effect":"Allow",`+
			`"Action":["sts:AssumeRole","sts:TagSession"],"Resource":%q}]}`,
		svc.Action, c.roleARN(c.Name+"-ack-project-*"))

	trust := fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
			`"Principal":{"Service":"pods.eks.amazonaws.com"},`+
			`"Action":["sts:AssumeRole","sts:TagSession"],`+
			`"Condition":{"StringEquals":{"aws:SourceAccount":%q,`+
			`"aws:RequestTag/kubernetes-namespace":%q,`+
			`"aws:RequestTag/kubernetes-service-account":%q},`+
			`"ArnEquals":{"aws:SourceArn":%q}}}]}`,
		c.AccountID, ns, sa, c.ARN())

	goCtx := ctx.Context()
	imports := podidentity.Imports{}

	if ok, err := o.Lookup.PolicyExists(goCtx, policyARN); err != nil {
		return fmt.Errorf("look up policy: %w", err)
	} else if ok {
		imports.Policy = policyARN
	}

	if ok, err := o.Lookup.RoleExists(goCtx, name); err != nil {
		return fmt.Errorf("look up role: %w", err)
	} else if ok {
		imports.Role = name
		imports.Attachment = name + "/" + policyARN
	}

	id, err := o.Lookup.AssociationID(goCtx, c.Name, ns, sa)
	if err != nil {
		return fmt.Errorf("list pod identity associations: %w", err)
	}

	if id != "" {
		imports.Associations = map[string]string{sa: fmt.Sprintf("%s,%s", c.Name, id)}
	}

	// Tags another tool set on an adopted association stay
	// (IgnoreAssociationTags): stripping them in the import operation poisons
	// the provider's raw state.
	_, err = podidentity.New(ctx, "ack-svc/"+name, &podidentity.Args{
		Provider:              o.Provider,
		ClusterName:           o.ClusterName,
		Region:                c.Region,
		Namespace:             ns,
		ServiceAccounts:       []string{sa},
		RoleName:              name,
		PermissionsBoundary:   c.policyARN(o.Boundary),
		TrustPolicy:           pulumi.String(trust),
		ManagedPolicy:         &podidentity.ManagedPolicy{Name: name, Description: svc.Description, Document: pulumi.String(policyDoc)},
		IgnoreAssociationTags: true,
		Imports:               imports,
		LegacyTopLevel:        true,
		Names: func(k podidentity.Child) string {
			switch k.Kind {
			case podidentity.KindPolicy:
				return "ack-svc/" + name + "/policy"
			case podidentity.KindAttachment:
				return "ack-svc/" + name + "/attachment"
			case podidentity.KindAssociation:
				return "ack-svc/" + name + "/pia"
			default:
				return "ack-svc/" + name + "/role"
			}
		},
	})

	return err
}

// ProjectRole is one project's capability role.
type ProjectRole struct {
	// Name is the project's name: the role is <cluster>-ack-project-<Name>
	// and the policy scopes to resources carrying the name. Required.
	Name string
	// SSMPrefix is the parameter-store prefix the role may use, without a
	// trailing slash. Empty means "/business/<Name>".
	SSMPrefix string
}

// ProjectRolesOptions are the inputs of ProjectRoles.
type ProjectRolesOptions struct {
	// Boundary is the NAME of the permissions boundary of the project roles
	// themselves (the builders): one that requires the project and cluster
	// tags on iam:CreateRole and entraps the roles the controllers create
	// under it. Changing it moves the live roles to the new boundary in place.
	// Required.
	Boundary string
	// ForbiddenRoles are role-name patterns (`*` allowed) a project role may
	// NOT touch, whatever its project prefix would otherwise allow: a role the
	// platform's own identity mints for the project (an archive role, say).
	// An explicit deny, so it wins over the project-prefixed allow. Empty adds
	// nothing: the policy is unchanged.
	ForbiddenRoles []string
	// Controllers are the short names of the controllers allowed to assume
	// the roles. Nil means DefaultProjectControllers.
	Controllers []string
	// ResourceOptions apply to every resource, normally the AWS provider.
	ResourceOptions []pulumi.ResourceOption

	// The fields below are opt-in tightenings of the role's identity policy.
	// All zero: the policy is the one earlier releases rendered, plus the
	// KMS key creation grant (see ProjectRoles).

	// WorkloadBoundary is the NAME of the one permissions boundary the project
	// may put on the roles it creates (and set with iam:PutRolePermissionsBoundary).
	// Set, an explicit deny refuses iam:CreateRole and
	// iam:PutRolePermissionsBoundary with any other boundary, or none. Empty:
	// the identity policy names none and the role's own Boundary alone limits
	// what a created role may carry.
	WorkloadBoundary string
	// ProtectTags denies changing or removing the project and cluster tags:
	// setting either to a value other than this project's (iam:CreateRole,
	// iam:CreatePolicy, iam:TagRole, iam:TagPolicy, kms:CreateKey,
	// kms:TagResource, eks:TagResource), removing either from any role, policy,
	// key or association, re-tagging a role or key that carries another
	// project's tag, and touching the project role itself (tags, policies,
	// trust, boundary, deletion). Boundaries and workload roles key on those
	// two tags, so a project that could rewrite them could reach another
	// project's data.
	ProtectTags bool
	// RestrictKMS replaces the "kms:*" grant on keys tagged with the project
	// by the key-management actions a project needs (describe, tag, rotation,
	// aliases, description, enable) and denies the rest that decides who owns
	// a key or when it dies: kms:PutKeyPolicy, kms:CreateGrant,
	// kms:RetireGrant, kms:RevokeGrant, kms:ScheduleKeyDeletion and
	// kms:DisableKey. Key policy, grants and retirement are then a platform
	// act, not a project's.
	RestrictKMS bool
	// DropSSM removes the "ssm:*" grant on the project's parameter prefix.
	// SSMPrefix of the projects is then unused.
	DropSSM bool
	// DropDynamoDB removes the dynamodb grants (account reads and "dynamodb:*"
	// on the project's tables).
	DropDynamoDB bool
	// PodIdentity adds what a builder needs to be the identity of the EKS
	// controller (IAMRoleSelector on ack-eks): the Pod Identity association
	// calls on this cluster's associations, and iam:PassRole of the project's
	// roles to pods.eks.amazonaws.com. Add "eks" to Controllers too, or
	// ack-eks cannot assume the role.
	PodIdentity bool
	// Tighten is shorthand for ProtectTags, RestrictKMS, DropSSM and
	// DropDynamoDB. It does not turn on PodIdentity or set WorkloadBoundary.
	Tighten bool
}

func (o ProjectRolesOptions) tightening() tightening {
	return tightening{
		workloadBoundary: o.WorkloadBoundary,
		protectTags:      o.ProtectTags || o.Tighten,
		restrictKMS:      o.RestrictKMS || o.Tighten,
		dropSSM:          o.DropSSM || o.Tighten,
		dropDynamoDB:     o.DropDynamoDB || o.Tighten,
		podIdentity:      o.PodIdentity,
	}
}

// DefaultProjectControllers are the controllers that may assume a project role.
func DefaultProjectControllers() []string {
	return []string{"iam", "s3", "kms", "dynamodb"}
}

// ProjectRoles mints one {cluster}-ack-project-{name} role per entry: the
// controllers assume it for the CRs of the project's namespace. The service
// controllers' roles are created later (by the IAM controller, in two phases),
// so the trust names the account root with an ArnEquals condition on the
// controller roles: robust to a controller role being recreated.
//
// The identity policy lets the role create a KMS key tagged with its own
// project (kms:CreateKey and kms:TagResource under aws:RequestTag/project): a
// key has no tag before it exists, so the grant on keys that carry the tag
// can never allow the first creation. It also denies re-tagging a key that
// carries another project's tag. The options of ProjectRolesOptions tighten the
// rest; none set, the policy is otherwise what earlier releases rendered.
func ProjectRoles(ctx *pulumi.Context, c Cluster, o ProjectRolesOptions, projects []ProjectRole) error {
	if err := c.Validate(); err != nil {
		return err
	}

	if o.Boundary == "" {
		return errors.New("options: Boundary is empty")
	}

	for _, p := range projects {
		if p.Name == "" {
			return errors.New("project role: Name is empty")
		}

		ssm := "/business/" + p.Name
		if p.SSMPrefix != "" {
			ssm = p.SSMPrefix
		}

		policy := projectPolicy(c, p.Name, ssm, o.ForbiddenRoles, o.tightening())

		if err := newBuilderRole(ctx, c, o.Boundary, p.Name, o.Controllers, policy, o.ResourceOptions); err != nil {
			return fmt.Errorf("project role for %s: %w", p.Name, err)
		}
	}

	return nil
}

// newBuilderRole registers {cluster}-ack-project-{name}, tagged with the
// project and cluster, trusting the controllers, with its inline policy. The
// identity policy scopes the builder; the boundary supplies the entrapment
// (the tag requirement and the child-boundary allow-list).
func newBuilderRole(ctx *pulumi.Context, c Cluster, boundary, name string, controllers []string, policy string, ro []pulumi.ResourceOption) error {
	if controllers == nil {
		controllers = DefaultProjectControllers()
	}

	var arns string

	for i, ctl := range controllers {
		if i > 0 {
			arns += ", "
		}

		arns += fmt.Sprintf("%q", c.roleARN(fmt.Sprintf("%s-ack-%s", c.Name, ctl)))
	}

	roleName := fmt.Sprintf("%s-ack-project-%s", c.Name, name)

	trust := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"AWS": %q},
    "Action": ["sts:AssumeRole", "sts:TagSession"],
    "Condition": {"ArnEquals": {"aws:PrincipalArn": [%s]}}
  }]
}`, c.iamARN("root"), arns)

	role, err := iam.NewRole(ctx, roleName, &iam.RoleArgs{
		Name:                pulumi.String(roleName),
		AssumeRolePolicy:    pulumi.String(trust),
		PermissionsBoundary: pulumi.String(c.policyARN(boundary)),
		Tags: pulumi.StringMap{
			"project": pulumi.String(name),
			"cluster": pulumi.String(c.Name),
		},
	}, ro...)
	if err != nil {
		return fmt.Errorf("create role: %w", err)
	}

	if _, err := iam.NewRolePolicy(ctx, roleName+"-policy", &iam.RolePolicyArgs{
		Role:   role.Name,
		Name:   pulumi.String(roleName + "-policy"),
		Policy: pulumi.String(policy),
	}, ro...); err != nil {
		return fmt.Errorf("attach policy: %w", err)
	}

	return nil
}
