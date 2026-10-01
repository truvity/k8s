package eksoidc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/eks"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:k8s/aws:EksOidc"

// Defaults of the optional Args fields.
const (
	// DefaultUsernameClaim is the claim the username is taken from.
	DefaultUsernameClaim = "sub"
	// DefaultUsernamePrefix is "-", EKS's documented value for no prefix at
	// all: left unset EKS prefixes the username with the issuer, which turns
	// every binding made on a bare name into a stranger's name.
	DefaultUsernamePrefix = "-"
	// DefaultGroupsClaim is the claim the groups are taken from.
	DefaultGroupsClaim = "groups"
)

// Kind names which child a logical name is for.
type Kind string

const (
	// KindProviderConfig is the eks.IdentityProviderConfig.
	KindProviderConfig Kind = "provider-config"
)

// Child identifies one child for a NameFunc.
type Child struct {
	// Component is the logical name the component was registered with.
	Component string
	Kind      Kind
}

// NameFunc returns the Pulumi logical name of a child. The returned name must
// not be empty; it is part of the child's URN.
type NameFunc func(Child) string

// DefaultName names the association "<c>-oidc". The name is API.
func DefaultName(c Child) string { return c.Component + "-oidc" }

// Args configures the component.
type Args struct {
	// Provider is the AWS provider of the cluster's account and region.
	// Required: the component never falls back to a default provider.
	Provider pulumi.ProviderResource

	// ClusterName is the EKS cluster. Pass the cluster's Name output to order
	// the association after the cluster. Required.
	ClusterName pulumi.StringInput

	// ConfigName is the association's name inside EKS
	// (identityProviderConfigName). Required.
	ConfigName string
	// IssuerURL is the issuer, an https URL. Required.
	IssuerURL string
	// ClientID is the audience the issuer mints for this cluster. Required.
	ClientID string

	// UsernameClaim, UsernamePrefix and GroupsClaim say where the username and
	// the groups come from. Empty means DefaultUsernameClaim,
	// DefaultUsernamePrefix and DefaultGroupsClaim.
	UsernameClaim  string
	UsernamePrefix string
	GroupsClaim    string

	// Names overrides the logical name of the child. Nil uses DefaultName.
	Names NameFunc
	// LegacyTopLevel makes the child carry an alias from the URN it has when
	// it is registered directly under the stack (no parent), with the same
	// type and the name Names gives it. Set it when adopting an association
	// that was created before it was wrapped in this component.
	LegacyTopLevel bool
	// Protect marks the association protected, so a preview that would delete
	// or replace it fails. Nil (the default) protects; point it at false only
	// for a cluster that is meant to be torn down.
	Protect *bool
}

func (a *Args) protect() bool { return a.Protect == nil || *a.Protect }

func (a *Args) usernameClaim() string  { return orDefault(a.UsernameClaim, DefaultUsernameClaim) }
func (a *Args) usernamePrefix() string { return orDefault(a.UsernamePrefix, DefaultUsernamePrefix) }
func (a *Args) groupsClaim() string    { return orDefault(a.GroupsClaim, DefaultGroupsClaim) }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}

	return v
}

// EksOidc is the component.
type EksOidc struct {
	pulumi.ResourceState

	// ProviderConfig is the association, for whoever orders work after it.
	ProviderConfig *eks.IdentityProviderConfig
}

func (a *Args) nameFunc() NameFunc {
	if a.Names == nil {
		return DefaultName
	}

	return a.Names
}

// Validate reports every problem with args at once, or returns nil.
func (a *Args) Validate() error {
	var errs []error

	if a.Provider == nil {
		errs = append(errs, errors.New("args: Provider is nil"))
	}

	if a.ClusterName == nil {
		errs = append(errs, errors.New("args: ClusterName is nil"))
	}

	if a.ConfigName == "" {
		errs = append(errs, errors.New("args: ConfigName is empty"))
	}

	if a.ClientID == "" {
		errs = append(errs, errors.New("args: ClientID is empty"))
	}

	switch {
	case a.IssuerURL == "":
		errs = append(errs, errors.New("args: IssuerURL is empty"))
	case !strings.HasPrefix(a.IssuerURL, "https://") || len(a.IssuerURL) == len("https://"):
		errs = append(errs, fmt.Errorf("args: IssuerURL %q is not an https URL", a.IssuerURL))
	}

	errs = append(errs, a.checkNames("")...)

	return errors.Join(errs...)
}

// checkNames refuses a naming hook that returns an empty name.
func (a *Args) checkNames(component string) []error {
	if a.nameFunc()(Child{Component: component, Kind: KindProviderConfig}) == "" {
		return []error{errors.New("args: Names returned an empty name for provider-config")}
	}

	return nil
}

// New registers the component and its child. It returns an error, registering
// nothing, when args.Validate does or when Names returns an empty name.
//
// The provider comes from Args, not from pulumi.Providers.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*EksOidc, error) {
	if args == nil {
		return nil, errors.New("eksoidc: args is nil")
	}

	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("eksoidc %s: %w", name, err)
	}

	if err := errors.Join(args.checkNames(name)...); err != nil {
		return nil, fmt.Errorf("eksoidc %s: %w", name, err)
	}

	comp := &EksOidc{}
	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	childOpts := []pulumi.ResourceOption{
		pulumi.Parent(comp),
		pulumi.Provider(args.Provider),
		// EKS admits one external OIDC association per cluster: a replace
		// has to disassociate the old one first.
		pulumi.DeleteBeforeReplace(true),
	}

	if args.LegacyTopLevel {
		childOpts = append(childOpts, pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}}))
	}

	if args.protect() {
		childOpts = append(childOpts, pulumi.Protect(true))
	}

	cfg, err := eks.NewIdentityProviderConfig(ctx, args.nameFunc()(Child{Component: name, Kind: KindProviderConfig}), &eks.IdentityProviderConfigArgs{
		ClusterName: args.ClusterName,
		Oidc: &eks.IdentityProviderConfigOidcArgs{
			ClientId:                   pulumi.String(args.ClientID),
			IdentityProviderConfigName: pulumi.String(args.ConfigName),
			IssuerUrl:                  pulumi.String(args.IssuerURL),
			UsernameClaim:              pulumi.StringPtr(args.usernameClaim()),
			UsernamePrefix:             pulumi.StringPtr(args.usernamePrefix()),
			GroupsClaim:                pulumi.StringPtr(args.groupsClaim()),
		},
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("eksoidc %s: create identity provider config: %w", name, err)
	}

	comp.ProviderConfig = cfg

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{
		"providerConfigArn": cfg.Arn,
	}); err != nil {
		return nil, err
	}

	return comp, nil
}
