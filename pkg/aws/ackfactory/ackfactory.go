package ackfactory

import (
	"context"
	"encoding/json"
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

// trustPodIdentity is the trust policy that lets EKS Pod Identity assume a
// role, pinned to the cluster: SourceAccount alone would admit any Pod
// Identity principal in the account, another cluster's included.
func trustPodIdentity(ctx *pulumi.Context, provider *aws.Provider, accountID, clusterARN string) (string, error) {
	return policyJSON(ctx, provider, []iam.GetPolicyDocumentStatement{
		{
			Effect: pulumi.StringRef("Allow"),
			Principals: []iam.GetPolicyDocumentStatementPrincipal{
				{Type: "Service", Identifiers: []string{"pods.eks.amazonaws.com"}},
			},
			Actions: []string{actionAssumeRole, actionTagSession},
			Conditions: []iam.GetPolicyDocumentStatementCondition{
				{Test: "StringEquals", Variable: "aws:SourceAccount", Values: []string{accountID}},
				{Test: "ArnEquals", Variable: "aws:SourceArn", Values: []string{clusterARN}},
			},
		},
	})
}

// IAM deploys the {cluster}-ack-iam role and its association: the
// controller that mints every other IAM role in the bundle.
//
// WithProjectAssume additionally lets the controller assume the per-project
// capability roles ({cluster}-ack-project-*): set it on clusters that host
// projects.
//
// It returns the identity; the caller exports the role ARN if it wants to.
func IAM(ctx *pulumi.Context, c Cluster, o Options, withProjectAssume bool) (*podidentity.PodIdentity, error) {
	if err := o.validate(c); err != nil {
		return nil, err
	}

	trust, err := trustPodIdentity(ctx, o.Provider, c.AccountID, c.ARN())
	if err != nil {
		return nil, fmt.Errorf("build ACK IAM trust policy: %w", err)
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
		TrustPolicy:         pulumi.String(trust),
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
// It returns the identity; the caller exports the role ARN if it wants to.
func EKS(ctx *pulumi.Context, c Cluster, o Options) (*podidentity.PodIdentity, error) {
	if err := o.validate(c); err != nil {
		return nil, err
	}

	trust, err := trustPodIdentity(ctx, o.Provider, c.AccountID, c.ARN())
	if err != nil {
		return nil, fmt.Errorf("build ACK EKS trust policy: %w", err)
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
		TrustPolicy:         pulumi.String(trust),
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
	// Boundary is the NAME of the permissions boundary of the project roles:
	// one that requires the project and cluster tags on iam:CreateRole and
	// entraps the roles the controllers create under it. Required.
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
func ProjectRoles(ctx *pulumi.Context, c Cluster, o ProjectRolesOptions, projects []ProjectRole) error {
	if err := c.Validate(); err != nil {
		return err
	}

	if o.Boundary == "" {
		return errors.New("options: Boundary is empty")
	}

	controllers := o.Controllers
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

	for _, p := range projects {
		if p.Name == "" {
			return errors.New("project role: Name is empty")
		}

		name := p.Name
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
			PermissionsBoundary: pulumi.String(c.policyARN(o.Boundary)),
			Tags: pulumi.StringMap{
				"project": pulumi.String(name),
				"cluster": pulumi.String(c.Name),
			},
		}, o.ResourceOptions...)
		if err != nil {
			return fmt.Errorf("create ack-project role for %s: %w", name, err)
		}

		ssm := "/business/" + name
		if p.SSMPrefix != "" {
			ssm = p.SSMPrefix
		}

		// The identity policy scopes to the project; the boundary supplies the
		// entrapment (the tag requirement and the child-boundary allow-list).
		if _, err := iam.NewRolePolicy(ctx, roleName+"-policy", &iam.RolePolicyArgs{
			Role:   role.Name,
			Name:   pulumi.String(roleName + "-policy"),
			Policy: pulumi.String(projectPolicy(c, name, ssm, o.ForbiddenRoles)),
		}, o.ResourceOptions...); err != nil {
			return fmt.Errorf("attach ack-project policy for %s: %w", name, err)
		}
	}

	return nil
}

// projectPolicy is a project role's identity policy.
func projectPolicy(c Cluster, name, ssmPrefix string, forbidden []string) string {
	return withForbiddenRoles(c, forbidden, fmt.Sprintf(`{
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
}`, c.AccountID, name, ssmPrefix, c.partition()))
}

// withForbiddenRoles appends the explicit deny for ForbiddenRoles to a policy
// document, or returns it as it is when there are none.
func withForbiddenRoles(c Cluster, forbidden []string, doc string) string {
	if len(forbidden) == 0 {
		return doc
	}

	resources := make([]string, 0, len(forbidden))
	for _, pattern := range forbidden {
		resources = append(resources, c.roleARN(pattern))
	}

	var policy map[string]any
	if err := json.Unmarshal([]byte(doc), &policy); err != nil {
		panic(fmt.Sprintf("ackfactory: project policy is not JSON: %v", err))
	}

	policy["Statement"] = append(policy["Statement"].([]any), map[string]any{
		"Sid":      "DenyPlatformMintedRoles",
		"Effect":   "Deny",
		"Action":   "iam:*",
		"Resource": resources,
	})

	out, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		panic(err)
	}

	return string(out)
}
