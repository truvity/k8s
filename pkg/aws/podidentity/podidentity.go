package podidentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/oidcissuer"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:k8s/aws:PodIdentity"

// Mode says how pods prove the role: EKS Pod Identity (the default) or IRSA,
// a federated web identity from an OIDC issuer.
type Mode string

const (
	// ModePodIdentity is the default: the role trusts the EKS Pod Identity
	// service and one association per service account binds it.
	ModePodIdentity Mode = ""
	// ModeIRSA trusts the cluster's OIDC provider through
	// sts:AssumeRoleWithWebIdentity and registers no association: the pod's
	// ServiceAccount carries RoleARNAnnotation instead.
	ModeIRSA Mode = "irsa"
)

// RoleARNAnnotation is the ServiceAccount annotation that the pod identity
// webhook reads to inject a role's credentials in ModeIRSA.
const RoleARNAnnotation = "eks.amazonaws.com/role-arn"

// DefaultPartition is Args.Partition when it is empty.
const DefaultPartition = "aws"

// Kind names which child a logical name is for.
type Kind string

const (
	// KindRole is the iam.Role.
	KindRole Kind = "role"
	// KindRolePolicy is the iam.RolePolicy built from Args.InlinePolicy.
	KindRolePolicy Kind = "role-policy"
	// KindPolicy is the iam.Policy built from Args.ManagedPolicy.
	KindPolicy Kind = "policy"
	// KindAttachment is the iam.RolePolicyAttachment of that managed policy.
	KindAttachment Kind = "attachment"
	// KindAssociation is an eks.PodIdentityAssociation; Child.Key is its
	// service account.
	KindAssociation Kind = "association"
)

// Child identifies one child for a NameFunc.
type Child struct {
	// Component is the logical name the component was registered with.
	Component string
	Kind      Kind
	// Key is the service account of a KindAssociation. Empty otherwise.
	Key string
}

// NameFunc returns the Pulumi logical name of a child. The returned name must
// be unique among the component's children; it is part of the child's URN.
type NameFunc func(Child) string

// DefaultName names the children from the component name: "<c>-role",
// "<c>-role-policy", "<c>-policy", "<c>-attachment" and "<c>-pia-<sa>". These
// names are API.
func DefaultName(c Child) string {
	switch c.Kind {
	case KindRolePolicy:
		return c.Component + "-role-policy"
	case KindPolicy:
		return c.Component + "-policy"
	case KindAttachment:
		return c.Component + "-attachment"
	case KindAssociation:
		return c.Component + "-pia-" + c.Key
	default:
		return c.Component + "-role"
	}
}

// InlinePolicy is a policy embedded in the role (iam.RolePolicy).
type InlinePolicy struct {
	// Name is the inline policy's name within the role. Required.
	Name string
	// Document is the policy JSON. Required.
	Document pulumi.StringInput
}

// ManagedPolicy is a customer-managed policy the component creates and
// attaches to the role.
type ManagedPolicy struct {
	// Name is the policy's name. Required.
	Name string
	// Description is the policy's description. It cannot be changed in IAM
	// once set, so adopting an existing policy needs the live value.
	Description string
	// Document is the policy JSON. Required.
	Document pulumi.StringInput
}

// Imports carries the IDs of resources that already exist, to adopt rather
// than create. An empty value creates the child. The IDs must be known when
// the program declares the resource, so the caller looks them up first.
//
// An association's import ID ("<cluster>,<association id>") differs from the
// ID the provider keeps in state (the bare association id): keep an import
// option on an association that is already in state and the next preview
// plans a replacement. Set Associations only while adopting.
type Imports struct {
	// Role is the role name.
	Role string
	// Policy is the managed policy's ARN.
	Policy string
	// Attachment is "<role name>/<policy ARN>".
	Attachment string
	// Associations maps a service account to its association's import ID.
	Associations map[string]string
}

// Args configures the component.
type Args struct {
	// Provider is the AWS provider of the cluster's account and region.
	// Required: the component never falls back to a default provider.
	Provider pulumi.ProviderResource

	// ClusterName is the EKS cluster the associations are in. Pass the
	// cluster's Name output to order the associations after the cluster.
	// Required in the Pod Identity mode; ModeIRSA registers no association,
	// so it refuses one.
	ClusterName pulumi.StringInput
	// Region is set on each association when it is not empty; empty leaves
	// the provider's region in force. Pod Identity mode only.
	Region string

	// Namespace and ServiceAccounts say which pods may assume the role: one
	// association per service account in the Pod Identity mode, one entry of
	// the trust policy's subject in ModeIRSA. Required, no repeats.
	Namespace       string
	ServiceAccounts []string

	// RoleName is the IAM role's name. Required.
	RoleName string
	// PermissionsBoundary is the ARN of the role's permissions boundary.
	// Empty sets none.
	PermissionsBoundary string

	// AccountID and ClusterARN pin the default trust policy to the cluster:
	// the source account and the source ARN EKS stamps on every assume.
	// Required unless TrustPolicy is set. In ModeIRSA only AccountID is read
	// (it names the account of the OIDC provider, unless ProviderARN is
	// given) and ClusterARN is refused.
	AccountID  string
	ClusterARN string
	// TrustPolicy replaces the default trust policy and is used verbatim.
	// Use it to adopt a role whose trust document was rendered another way:
	// IAM compares the JSON, but the provider compares the string, so a
	// re-rendered document is an in-place update of the role.
	TrustPolicy pulumi.StringInput

	// InlinePolicy and ManagedPolicy are the role's permissions. Either,
	// both or neither.
	InlinePolicy  *InlinePolicy
	ManagedPolicy *ManagedPolicy

	// IgnoreAssociationTags ignores changes to the associations' tags and
	// tagsAll: for associations adopted with tags another tool set. Pod
	// Identity mode only.
	IgnoreAssociationTags bool
	// Imports adopts existing resources. Imports.Associations is Pod Identity
	// mode only.
	Imports Imports

	// Mode picks Pod Identity (empty, the default) or ModeIRSA.
	Mode Mode
	// Issuer, Partition and ProviderARN are for ModeIRSA and refused in the
	// Pod Identity mode.
	//
	// Issuer is the cluster's service account issuer URL: https, without a
	// trailing slash. It is the OIDC provider's URL, and its host and path
	// key the trust policy's conditions. Required unless TrustPolicy is set.
	Issuer string
	// Partition is the ARN partition of the derived provider ARN. Empty
	// means DefaultPartition.
	Partition string
	// ProviderARN is the ARN of the IAM OIDC provider for Issuer. Empty
	// derives it from the partition, AccountID and Issuer, which is the ARN
	// of the provider that oidcissuer registers.
	ProviderARN string

	// Names overrides the logical names of the children. Nil uses
	// DefaultName.
	Names NameFunc
	// Protect marks the role, the policy and the associations protected, so
	// a preview that would delete or replace them fails. False (the default)
	// protects nothing.
	Protect bool
}

// PodIdentity is the component.
type PodIdentity struct {
	pulumi.ResourceState

	// RoleARN and RoleName are the role's.
	RoleARN  pulumi.StringOutput
	RoleName pulumi.StringOutput
}

// plan lists every child the args register, in registration order.
func (a *Args) plan(component string) []Child {
	var out []Child

	if a.ManagedPolicy != nil {
		out = append(out, Child{Component: component, Kind: KindPolicy})
	}

	out = append(out, Child{Component: component, Kind: KindRole})

	if a.InlinePolicy != nil {
		out = append(out, Child{Component: component, Kind: KindRolePolicy})
	}

	if a.ManagedPolicy != nil {
		out = append(out, Child{Component: component, Kind: KindAttachment})
	}

	if a.Mode == ModeIRSA {
		return out
	}

	for _, sa := range a.ServiceAccounts {
		out = append(out, Child{Component: component, Kind: KindAssociation, Key: sa})
	}

	return out
}

// Validate reports every problem with args at once, or returns nil.
func (a *Args) Validate() error {
	var errs []error

	if a.Provider == nil {
		errs = append(errs, errors.New("args: Provider is nil"))
	}

	errs = append(errs, a.checkMode()...)

	if a.Mode == ModePodIdentity && a.ClusterName == nil {
		errs = append(errs, errors.New("args: ClusterName is nil"))
	}

	if a.RoleName == "" {
		errs = append(errs, errors.New("args: RoleName is empty"))
	}

	if a.Namespace == "" {
		errs = append(errs, errors.New("args: Namespace is empty"))
	}

	if len(a.ServiceAccounts) == 0 {
		errs = append(errs, errors.New("args: ServiceAccounts is empty: a role nobody can assume is not an identity"))
	}

	seen := map[string]bool{}

	for i, sa := range a.ServiceAccounts {
		switch {
		case sa == "":
			errs = append(errs, fmt.Errorf("args: ServiceAccounts[%d] is empty", i))
		case seen[sa]:
			errs = append(errs, fmt.Errorf("args: ServiceAccounts[%d]: duplicate %q", i, sa))
		}

		seen[sa] = true
	}

	if a.TrustPolicy == nil && a.Mode == ModePodIdentity {
		if a.AccountID == "" {
			errs = append(errs, errors.New("args: AccountID is empty and no TrustPolicy is given"))
		}

		if a.ClusterARN == "" {
			errs = append(errs, errors.New("args: ClusterARN is empty and no TrustPolicy is given"))
		}
	}

	if a.TrustPolicy == nil && a.Mode == ModeIRSA {
		errs = append(errs, a.checkIRSATrust()...)
	}

	if p := a.InlinePolicy; p != nil {
		if p.Name == "" {
			errs = append(errs, errors.New("args: InlinePolicy.Name is empty"))
		}

		if p.Document == nil {
			errs = append(errs, errors.New("args: InlinePolicy.Document is nil"))
		}
	}

	if p := a.ManagedPolicy; p != nil {
		if p.Name == "" {
			errs = append(errs, errors.New("args: ManagedPolicy.Name is empty"))
		}

		if p.Document == nil {
			errs = append(errs, errors.New("args: ManagedPolicy.Document is nil"))
		}
	}

	for sa := range a.Imports.Associations {
		if a.Mode == ModePodIdentity && !seen[sa] {
			errs = append(errs, fmt.Errorf("args: Imports.Associations names %q, which is not in ServiceAccounts", sa))
		}
	}

	errs = append(errs, a.checkNames("")...)

	return errors.Join(errs...)
}

// checkMode refuses an unknown Mode, and every field the mode would ignore
// silently: a field that does nothing is a mistake the caller cannot see.
func (a *Args) checkMode() []error {
	var errs []error

	switch a.Mode {
	case ModePodIdentity:
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"Issuer", a.Issuer != ""},
			{"ProviderARN", a.ProviderARN != ""},
			{"Partition", a.Partition != ""},
		} {
			if f.set {
				errs = append(errs, fmt.Errorf("args: %s is set, but only Mode %q reads it", f.name, ModeIRSA))
			}
		}
	case ModeIRSA:
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"ClusterName", a.ClusterName != nil},
			{"ClusterARN", a.ClusterARN != ""},
			{"Region", a.Region != ""},
			{"IgnoreAssociationTags", a.IgnoreAssociationTags},
			{"Imports.Associations", len(a.Imports.Associations) > 0},
		} {
			if f.set {
				errs = append(errs, fmt.Errorf("args: %s is set, but Mode %q registers no association", f.name, ModeIRSA))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("args: unknown Mode %q: it is empty (Pod Identity) or %q", a.Mode, ModeIRSA))
	}

	return errs
}

// checkIRSATrust refuses what the IRSA trust policy cannot be rendered from:
// the issuer, the account or provider ARN, and a wildcard service account.
func (a *Args) checkIRSATrust() []error {
	var errs []error

	if a.Issuer == "" {
		errs = append(errs, errors.New("args: Issuer is empty and no TrustPolicy is given"))
	} else if err := checkIssuer(a.Issuer); err != nil {
		errs = append(errs, err)
	}

	if a.ProviderARN == "" && a.AccountID == "" {
		errs = append(errs, errors.New("args: AccountID is empty and no ProviderARN or TrustPolicy is given"))
	}

	for i, sa := range a.ServiceAccounts {
		if strings.ContainsAny(sa, "*?") {
			errs = append(errs, fmt.Errorf("args: ServiceAccounts[%d]: %q is a wildcard; write the trust policy and pass it as TrustPolicy", i, sa))
		}
	}

	return errs
}

// checkIssuer accepts an https URL with a host, without a query, a fragment
// or a trailing slash: the provider's URL and the token's issuer claim carry
// none, and the trust policy's condition keys are built from its host and path.
func checkIssuer(issuer string) error {
	u, err := url.Parse(issuer)

	switch {
	case err != nil || u.Scheme != "https" || u.Host == "":
		return fmt.Errorf("args: Issuer %q is not an https URL", issuer)
	case u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.Contains(issuer, "?") || strings.Contains(issuer, "#"):
		return fmt.Errorf("args: Issuer %q has a query, a fragment or a user", issuer)
	case strings.HasSuffix(issuer, "/"):
		return fmt.Errorf("args: Issuer %q has a trailing slash; the issuer is the URL without it", issuer)
	}

	return nil
}

func (a *Args) nameFunc() NameFunc {
	if a.Names == nil {
		return DefaultName
	}

	return a.Names
}

// checkNames refuses a naming hook that returns an empty or repeated name.
func (a *Args) checkNames(component string) []error {
	names := a.nameFunc()

	var errs []error

	used := map[string]Child{}

	for _, c := range a.plan(component) {
		n := names(c)
		if n == "" {
			errs = append(errs, fmt.Errorf("args: Names returned an empty name for %s", describe(c)))
			continue
		}

		if other, dup := used[n]; dup {
			errs = append(errs, fmt.Errorf("args: Names returned %q for both %s and %s", n, describe(other), describe(c)))
		}

		used[n] = c
	}

	return errs
}

func describe(c Child) string {
	if c.Key != "" {
		return string(c.Kind) + " " + c.Key
	}

	return string(c.Kind)
}

// trustPolicy renders the default trust policy: the EKS Pod Identity service
// may assume the role, for this cluster's account and ARN and only for the
// namespace and service accounts. One service account is a string in the
// condition, several are a list, in the order given.
func (a *Args) trustPolicy() (string, error) {
	var sa any = a.ServiceAccounts[0]
	if len(a.ServiceAccounts) > 1 {
		sa = slices.Clone(a.ServiceAccounts)
	}

	raw, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": "pods.eks.amazonaws.com"},
			"Action":    []string{"sts:AssumeRole", "sts:TagSession"},
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"aws:SourceAccount":                         a.AccountID,
					"aws:RequestTag/kubernetes-namespace":       a.Namespace,
					"aws:RequestTag/kubernetes-service-account": sa,
				},
				"ArnEquals": map[string]any{
					"aws:SourceArn": a.ClusterARN,
				},
			},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("render trust policy: %w", err)
	}

	return string(raw), nil
}

// irsaTrustPolicy renders the trust policy of ModeIRSA: the cluster's OIDC
// provider may assume the role through sts:AssumeRoleWithWebIdentity, for the
// issuer's audience and only for the namespace's service accounts.
func (a *Args) irsaTrustPolicy() (string, error) {
	providerARN := a.ProviderARN

	if providerARN == "" {
		u, err := url.Parse(a.Issuer)
		if err != nil {
			return "", fmt.Errorf("render trust policy: %w", err)
		}

		partition := a.Partition
		if partition == "" {
			partition = DefaultPartition
		}

		providerARN = "arn:" + partition + ":iam::" + a.AccountID + ":oidc-provider/" + u.Host + u.Path
	}

	subjects := make([]string, len(a.ServiceAccounts))
	for i, sa := range a.ServiceAccounts {
		subjects[i] = a.Namespace + "/" + sa
	}

	return oidcissuer.TrustPolicy(providerARN, a.Issuer, "", subjects...)
}

// New registers the component and its children. It returns an error,
// registering nothing, when args.Validate does or when Names returns an empty
// or repeated name.
//
// The provider comes from Args, not from pulumi.Providers.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*PodIdentity, error) {
	if args == nil {
		return nil, errors.New("podidentity: args is nil")
	}

	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("podidentity %s: %w", name, err)
	}

	if err := errors.Join(args.checkNames(name)...); err != nil {
		return nil, fmt.Errorf("podidentity %s: %w", name, err)
	}

	trust := args.TrustPolicy
	if trust == nil {
		render := args.trustPolicy
		if args.Mode == ModeIRSA {
			render = args.irsaTrustPolicy
		}

		doc, err := render()
		if err != nil {
			return nil, fmt.Errorf("podidentity %s: %w", name, err)
		}

		trust = pulumi.String(doc)
	}

	comp := &PodIdentity{}
	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	b := &builder{ctx: ctx, comp: comp, args: args, name: name, names: args.nameFunc()}
	if err := b.build(trust); err != nil {
		return nil, err
	}

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{
		"roleArn":  comp.RoleARN,
		"roleName": comp.RoleName,
	}); err != nil {
		return nil, err
	}

	return comp, nil
}

type builder struct {
	ctx   *pulumi.Context
	comp  *PodIdentity
	args  *Args
	name  string
	names NameFunc
}

func (b *builder) child(c Child) string {
	c.Component = b.name

	return b.names(c)
}

// childOpts is what every child registers with: the component as parent, the
// provider, the adoption alias when asked for, and protection when asked for.
func (b *builder) childOpts(protect bool, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := append([]pulumi.ResourceOption{pulumi.Parent(b.comp), pulumi.Provider(b.args.Provider)}, extra...)

	if protect && b.args.Protect {
		out = append(out, pulumi.Protect(true))
	}

	return out
}

func importOpt(id string) []pulumi.ResourceOption {
	if id == "" {
		return nil
	}

	return []pulumi.ResourceOption{pulumi.Import(pulumi.ID(id))}
}

func (b *builder) build(trust pulumi.StringInput) error {
	a := b.args

	var policy *iam.Policy

	if m := a.ManagedPolicy; m != nil {
		var err error

		policy, err = iam.NewPolicy(b.ctx, b.child(Child{Kind: KindPolicy}), &iam.PolicyArgs{
			Name:        pulumi.String(m.Name),
			Description: pulumi.String(m.Description),
			Policy:      m.Document,
		}, b.childOpts(true, importOpt(a.Imports.Policy)...)...)
		if err != nil {
			return fmt.Errorf("create policy: %w", err)
		}
	}

	roleArgs := &iam.RoleArgs{
		Name:             pulumi.String(a.RoleName),
		AssumeRolePolicy: trust,
	}
	if a.PermissionsBoundary != "" {
		roleArgs.PermissionsBoundary = pulumi.String(a.PermissionsBoundary)
	}

	role, err := iam.NewRole(b.ctx, b.child(Child{Kind: KindRole}), roleArgs,
		b.childOpts(true, importOpt(a.Imports.Role)...)...)
	if err != nil {
		return fmt.Errorf("create role: %w", err)
	}

	b.comp.RoleARN, b.comp.RoleName = role.Arn, role.Name

	if p := a.InlinePolicy; p != nil {
		if _, err := iam.NewRolePolicy(b.ctx, b.child(Child{Kind: KindRolePolicy}), &iam.RolePolicyArgs{
			Name:   pulumi.String(p.Name),
			Role:   role.Name,
			Policy: p.Document,
		}, b.childOpts(false)...); err != nil {
			return fmt.Errorf("create inline policy: %w", err)
		}
	}

	if policy != nil {
		if _, err := iam.NewRolePolicyAttachment(b.ctx, b.child(Child{Kind: KindAttachment}), &iam.RolePolicyAttachmentArgs{
			Role:      role.Name,
			PolicyArn: policy.Arn,
		}, b.childOpts(false, importOpt(a.Imports.Attachment)...)...); err != nil {
			return fmt.Errorf("attach policy: %w", err)
		}
	}

	if a.Mode == ModeIRSA {
		return nil
	}

	for _, sa := range a.ServiceAccounts {
		pia := &eks.PodIdentityAssociationArgs{
			ClusterName:    a.ClusterName,
			Namespace:      pulumi.String(a.Namespace),
			ServiceAccount: pulumi.String(sa),
			RoleArn:        role.Arn,
		}
		if a.Region != "" {
			pia.Region = pulumi.String(a.Region)
		}

		var extra []pulumi.ResourceOption
		if a.IgnoreAssociationTags {
			extra = append(extra, pulumi.IgnoreChanges([]string{"tags", "tagsAll"}))
		}

		extra = append(extra, importOpt(a.Imports.Associations[sa])...)

		if _, err := eks.NewPodIdentityAssociation(b.ctx, b.child(Child{Kind: KindAssociation, Key: sa}), pia,
			b.childOpts(true, extra...)...); err != nil {
			return fmt.Errorf("associate %s/%s: %w", a.Namespace, sa, err)
		}
	}

	return nil
}
