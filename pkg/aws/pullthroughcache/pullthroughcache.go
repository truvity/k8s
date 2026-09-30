package pullthroughcache

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecr"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:k8s/aws:PullThroughCache"

// SecretNamePrefix is the name prefix ECR requires of a pull-through cache
// credential secret. It is not configurable.
const SecretNamePrefix = "ecr-pullthroughcache/"

// Kind names which child a logical name is for.
type Kind string

const (
	// KindRule is an ecr.PullThroughCacheRule.
	KindRule Kind = "rule"
	// KindSecret is a secretsmanager.Secret holding an upstream credential.
	KindSecret Kind = "secret"
	// KindSecretVersion is the secretsmanager.SecretVersion of that secret.
	KindSecretVersion Kind = "secret-version"
)

// Upstream is one upstream registry to cache.
type Upstream struct {
	// Prefix is the local repository namespace: a pull of
	// <registry>/<Prefix>/<image> caches <RegistryURL>/<image>. ECR accepts
	// 2 to 30 characters of lower-case letters, digits, '.', '_', '-' and '/'.
	Prefix string
	// RegistryURL is the upstream registry host, without a scheme.
	RegistryURL string
	// NeedsCredentials says the upstream must be pulled with credentials.
	// Args.Credentials must then hold an entry for Prefix.
	NeedsCredentials bool
}

// Credentials are the username and access token for one upstream.
type Credentials struct {
	Username pulumi.StringInput
	Token    pulumi.StringInput
}

// NameFunc returns the Pulumi logical name of a child. prefix is the
// Upstream.Prefix the child belongs to. The returned name must be unique
// among the component's children; it is part of the child's URN.
type NameFunc func(kind Kind, prefix string) string

// Args configures the component.
type Args struct {
	// Upstreams lists the registries to cache. At least one.
	Upstreams []Upstream
	// Credentials holds, by Upstream.Prefix, the credentials of every
	// upstream with NeedsCredentials. An entry for any other prefix is
	// refused.
	Credentials map[string]Credentials
	// Names overrides the logical names of the children. Nil uses
	// DefaultName.
	Names NameFunc
	// LegacyTopLevel makes every child carry an alias from the URN it has
	// when it is registered directly under the stack (no parent), with the
	// same type and the name Names gives it. Set it when adopting resources
	// that were created before they were wrapped in this component.
	LegacyTopLevel bool
}

// DefaultName names the children "ptc-rule-<prefix>", "ptc-secret-<prefix>"
// and "ptc-secret-version-<prefix>". These names are API.
func DefaultName(kind Kind, prefix string) string {
	switch kind {
	case KindSecret:
		return "ptc-secret-" + prefix
	case KindSecretVersion:
		return "ptc-secret-version-" + prefix
	default:
		return "ptc-rule-" + prefix
	}
}

// PullThroughCache is the component.
type PullThroughCache struct {
	pulumi.ResourceState

	// Prefixes is the local repository namespace of every rule, in the
	// order of Args.Upstreams.
	Prefixes pulumi.StringArrayOutput

	prefixes []string
}

// PrefixList returns Prefixes as plain values, known when the component is
// constructed.
func (p *PullThroughCache) PrefixList() []string {
	return append([]string(nil), p.prefixes...)
}

var prefixPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

// Validate reports every problem with args at once, or returns nil.
func (a *Args) Validate() error {
	var errs []error

	if len(a.Upstreams) == 0 {
		errs = append(errs, errors.New("args: Upstreams is empty"))
	}

	seen := map[string]bool{}
	needs := map[string]bool{}

	for i, u := range a.Upstreams {
		switch {
		case u.Prefix == "":
			errs = append(errs, fmt.Errorf("args: Upstreams[%d]: Prefix is empty", i))
			continue
		case len(u.Prefix) < 2 || len(u.Prefix) > 30 || !prefixPattern.MatchString(u.Prefix):
			errs = append(errs, fmt.Errorf("args: Upstreams[%d]: Prefix %q is not a valid ECR repository prefix", i, u.Prefix))
		}

		if seen[u.Prefix] {
			errs = append(errs, fmt.Errorf("args: Upstreams[%d]: duplicate Prefix %q", i, u.Prefix))
		}

		seen[u.Prefix] = true

		if u.RegistryURL == "" {
			errs = append(errs, fmt.Errorf("args: Upstreams[%d] (%q): RegistryURL is empty", i, u.Prefix))
		}

		if u.NeedsCredentials {
			needs[u.Prefix] = true

			c, ok := a.Credentials[u.Prefix]
			if !ok || c.Username == nil || c.Token == nil {
				errs = append(errs, fmt.Errorf("upstream %q needs credentials: Credentials[%q] must set Username and Token", u.Prefix, u.Prefix))
			}
		}
	}

	extra := make([]string, 0, len(a.Credentials))
	for prefix := range a.Credentials {
		extra = append(extra, prefix)
	}

	sort.Strings(extra)

	for _, prefix := range extra {
		if !needs[prefix] {
			errs = append(errs, fmt.Errorf("args: Credentials[%q]: no upstream with that Prefix sets NeedsCredentials", prefix))
		}
	}

	errs = append(errs, a.checkNames()...)

	return errors.Join(errs...)
}

// checkNames refuses a naming hook that returns an empty or repeated name.
func (a *Args) checkNames() []error {
	names := a.Names
	if names == nil {
		names = DefaultName
	}

	var errs []error

	used := map[string]string{}

	for _, u := range a.Upstreams {
		if u.Prefix == "" {
			continue
		}

		kinds := []Kind{KindRule}
		if u.NeedsCredentials {
			kinds = append(kinds, KindSecret, KindSecretVersion)
		}

		for _, k := range kinds {
			n := names(k, u.Prefix)
			if n == "" {
				errs = append(errs, fmt.Errorf("args: Names returned an empty name for %s of %q", k, u.Prefix))
				continue
			}

			key := string(k) + " of " + u.Prefix
			if other, dup := used[n]; dup {
				errs = append(errs, fmt.Errorf("args: Names returned %q for both %s and %s", n, other, key))
			}

			used[n] = key
		}
	}

	return errs
}

// NewPullThroughCache registers the component and its children. It returns
// an error, registering nothing, when args.Validate does.
//
// Pass the AWS provider with pulumi.Providers(p) so every child uses it.
func NewPullThroughCache(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*PullThroughCache, error) {
	if args == nil {
		return nil, errors.New("pullthroughcache: args is nil")
	}

	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("pullthroughcache %s: %w", name, err)
	}

	names := args.Names
	if names == nil {
		names = DefaultName
	}

	comp := &PullThroughCache{}
	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	childOpts := func(extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
		out := append([]pulumi.ResourceOption{pulumi.Parent(comp)}, extra...)
		if args.LegacyTopLevel {
			out = append(out, pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}}))
		}

		return out
	}

	prefixes := make([]string, 0, len(args.Upstreams))

	for _, u := range args.Upstreams {
		prefixes = append(prefixes, u.Prefix)

		ruleArgs := &ecr.PullThroughCacheRuleArgs{
			EcrRepositoryPrefix: pulumi.String(u.Prefix),
			UpstreamRegistryUrl: pulumi.String(u.RegistryURL),
		}

		var ruleExtra []pulumi.ResourceOption

		if u.NeedsCredentials {
			creds := args.Credentials[u.Prefix]

			payload := pulumi.All(creds.Username, creds.Token).ApplyT(func(v []any) (string, error) {
				b, err := json.Marshal(map[string]string{
					"username":    v[0].(string),
					"accessToken": v[1].(string),
				})

				return string(b), err
			}).(pulumi.StringOutput)

			// KmsKeyId is deliberately unset: ECR only supports the default
			// aws/secretsmanager key here.
			secret, err := secretsmanager.NewSecret(ctx, names(KindSecret, u.Prefix), &secretsmanager.SecretArgs{
				Name:        pulumi.String(SecretNamePrefix + u.Prefix),
				Description: pulumi.String("Upstream credentials for the " + u.Prefix + " ECR pull-through cache rule (read-public-only token)"),
			}, childOpts()...)
			if err != nil {
				return nil, fmt.Errorf("create %s secret: %w", u.Prefix, err)
			}

			version, err := secretsmanager.NewSecretVersion(ctx, names(KindSecretVersion, u.Prefix), &secretsmanager.SecretVersionArgs{
				SecretId:     secret.ID(),
				SecretString: pulumi.ToSecret(payload).(pulumi.StringOutput),
			}, childOpts()...)
			if err != nil {
				return nil, fmt.Errorf("create %s secret version: %w", u.Prefix, err)
			}

			ruleArgs.CredentialArn = secret.Arn
			// ECR treats a secret with no version as NOT FOUND: depend on both.
			ruleExtra = append(ruleExtra, pulumi.DependsOn([]pulumi.Resource{secret, version}))
		}

		if _, err := ecr.NewPullThroughCacheRule(ctx, names(KindRule, u.Prefix), ruleArgs, childOpts(ruleExtra...)...); err != nil {
			return nil, fmt.Errorf("create %s pull-through cache rule: %w", u.Prefix, err)
		}
	}

	comp.prefixes = prefixes
	comp.Prefixes = pulumi.ToStringArray(prefixes).ToStringArrayOutput()

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{"prefixes": comp.Prefixes}); err != nil {
		return nil, err
	}

	return comp, nil
}
