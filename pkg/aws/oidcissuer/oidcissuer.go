// Package oidcissuer publishes a cluster's ServiceAccount issuer documents
// (pkg/talos/oidc) to an S3-compatible bucket and, optionally, registers the
// issuer as an IAM OpenID Connect provider, as a Pulumi ComponentResource:
// IAM roles for service accounts for a cluster AWS does not run.
//
// The bucket is the caller's: it must serve the two objects at the issuer
// URL, publicly, over https (a public-read bucket policy on the prefix, or a
// CDN or custom domain in front of a private one). The objects go through
// ObjectProvider, which may be any S3-compatible store; the IAM provider
// through IAMProvider, an AWS provider, which may be another account.
package oidcissuer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/talos/oidc"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:k8s/aws:OidcIssuer"

// DefaultAudience is the audience AWS STS expects in a projected token.
const DefaultAudience = "sts.amazonaws.com"

// DefaultCacheControl keeps a key rotation visible within minutes.
const DefaultCacheControl = "max-age=300"

// Kind names which child a logical name is for.
type Kind string

const (
	// KindDiscovery is the s3.BucketObjectv2 of the discovery document.
	KindDiscovery Kind = "discovery"
	// KindJWKS is the s3.BucketObjectv2 of the key set.
	KindJWKS Kind = "jwks"
	// KindIAMProvider is the iam.OpenIdConnectProvider.
	KindIAMProvider Kind = "iam-provider"
)

// Child identifies one child for a NameFunc.
type Child struct {
	Component string
	Kind      Kind
}

// NameFunc returns the Pulumi logical name of a child.
type NameFunc func(Child) string

// DefaultName names the children "<c>-discovery", "<c>-jwks" and
// "<c>-iam-provider". These names are API.
func DefaultName(c Child) string { return c.Component + "-" + string(c.Kind) }

// Args configures the component.
type Args struct {
	// ObjectProvider is the AWS provider the objects are written through:
	// AWS S3, or an S3-compatible store configured as the provider's S3
	// endpoint. Required.
	ObjectProvider pulumi.ProviderResource
	// Bucket is the bucket the documents are written to. Required.
	Bucket string
	// KeyPrefix is the object key prefix. Nil: the issuer URL's path
	// without its leading slash (an issuer https://host/a/b writes a/b/...),
	// right when the bucket is served at the issuer host's root.
	KeyPrefix *string
	// ACL is a canned ACL for both objects ("public-read"). Empty sets none:
	// a bucket policy or a CDN makes them public.
	ACL string
	// CacheControl is the objects' Cache-Control. Empty: DefaultCacheControl.
	CacheControl string

	// Issuer is the ServiceAccount issuer URL; the same as the machine
	// config's. Required.
	Issuer string
	// Documents are the issuer's documents, from oidc.Generate. Required.
	Documents *oidc.Documents

	// IAMProvider registers the issuer as an IAM OpenID Connect provider
	// through this AWS provider. Nil registers none.
	IAMProvider pulumi.ProviderResource
	// Audiences are the IAM provider's client IDs. Empty: DefaultAudience.
	Audiences []string
	// Tags are set on the IAM provider.
	Tags map[string]string

	// Names overrides the children's logical names. Nil: DefaultName.
	Names NameFunc
}

// OidcIssuer is the component.
type OidcIssuer struct {
	pulumi.ResourceState

	// DiscoveryKey and JWKSKey are the objects' keys.
	DiscoveryKey string
	JWKSKey      string
	// ProviderARN is the IAM provider's ARN; empty without IAMProvider.
	ProviderARN pulumi.StringOutput
}

func (a *Args) prefix() string {
	if a.KeyPrefix != nil {
		return strings.Trim(*a.KeyPrefix, "/")
	}

	u, _ := url.Parse(a.Issuer) //nolint:errcheck // Validate parsed it

	return strings.Trim(u.Path, "/")
}

func (a *Args) key(path string) string {
	p := strings.TrimPrefix(path, "/")
	if pre := a.prefix(); pre != "" {
		return pre + "/" + p
	}

	return p
}

func (a *Args) audiences() []string {
	if len(a.Audiences) == 0 {
		return []string{DefaultAudience}
	}

	return a.Audiences
}

// Validate refuses what would publish documents nothing can use.
func (a *Args) Validate() error {
	var errs []error

	if a.ObjectProvider == nil {
		errs = append(errs, errors.New("args: ObjectProvider is nil"))
	}

	if a.Bucket == "" {
		errs = append(errs, errors.New("args: Bucket is empty"))
	}

	u, err := url.Parse(a.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(u.Path, "/") {
		errs = append(errs, fmt.Errorf("args: Issuer %q is not an https URL without a trailing slash", a.Issuer))
	}

	if a.Documents == nil || len(a.Documents.Discovery) == 0 || len(a.Documents.JWKS) == 0 {
		errs = append(errs, errors.New("args: Documents is empty"))
	} else if err == nil {
		var d struct {
			Issuer string `json:"issuer"`
		}

		if jerr := json.Unmarshal(a.Documents.Discovery, &d); jerr != nil || d.Issuer != a.Issuer {
			errs = append(errs, fmt.Errorf("args: the discovery document's issuer %q is not Issuer %q", d.Issuer, a.Issuer))
		}
	}

	for i, aud := range a.Audiences {
		if aud == "" || slices.Index(a.Audiences, aud) != i {
			errs = append(errs, fmt.Errorf("args: Audiences[%d] is empty or repeated", i))
		}
	}

	return errors.Join(errs...)
}

// New registers the component and its children.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*OidcIssuer, error) {
	if args == nil {
		return nil, errors.New("oidcissuer: args is nil")
	}

	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("oidcissuer %s: %w", name, err)
	}

	names := args.Names
	if names == nil {
		names = DefaultName
	}

	comp := &OidcIssuer{
		DiscoveryKey: args.key(oidc.DiscoveryPath),
		JWKSKey:      args.key(oidc.JWKSPath),
	}
	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	cache := args.CacheControl
	if cache == "" {
		cache = DefaultCacheControl
	}

	for _, o := range []struct {
		kind    Kind
		key     string
		content []byte
	}{
		{KindDiscovery, comp.DiscoveryKey, args.Documents.Discovery},
		{KindJWKS, comp.JWKSKey, args.Documents.JWKS},
	} {
		objArgs := &s3.BucketObjectv2Args{
			Bucket:       pulumi.String(args.Bucket),
			Key:          pulumi.String(o.key),
			Content:      pulumi.String(string(o.content)),
			ContentType:  pulumi.String("application/json"),
			CacheControl: pulumi.String(cache),
		}
		if args.ACL != "" {
			objArgs.Acl = pulumi.String(args.ACL)
		}

		if _, err := s3.NewBucketObjectv2(ctx, names(Child{Component: name, Kind: o.kind}), objArgs,
			pulumi.Parent(comp), pulumi.Provider(args.ObjectProvider)); err != nil {
			return nil, fmt.Errorf("oidcissuer %s: %s: %w", name, o.kind, err)
		}
	}

	comp.ProviderARN = pulumi.String("").ToStringOutput()

	if args.IAMProvider != nil {
		p, err := iam.NewOpenIdConnectProvider(ctx, names(Child{Component: name, Kind: KindIAMProvider}), &iam.OpenIdConnectProviderArgs{
			Url:           pulumi.String(args.Issuer),
			ClientIdLists: pulumi.ToStringArray(args.audiences()),
			Tags:          pulumi.ToStringMap(args.Tags),
		}, pulumi.Parent(comp), pulumi.Provider(args.IAMProvider))
		if err != nil {
			return nil, fmt.Errorf("oidcissuer %s: iam provider: %w", name, err)
		}

		comp.ProviderARN = p.Arn
	}

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{
		"discoveryKey": pulumi.String(comp.DiscoveryKey),
		"jwksKey":      pulumi.String(comp.JWKSKey),
		"providerArn":  comp.ProviderARN,
	}); err != nil {
		return nil, err
	}

	return comp, nil
}

// TrustPolicy is the assume-role policy that lets the named ServiceAccounts
// ("<namespace>/<name>", a "*" allowed in the name) assume a role through
// the issuer's IAM provider, for the audience (empty: DefaultAudience).
func TrustPolicy(providerARN, issuer, audience string, serviceAccounts ...string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("oidcissuer: issuer %q is not a URL", issuer)
	}

	if providerARN == "" || len(serviceAccounts) == 0 {
		return "", errors.New("oidcissuer: a provider ARN and at least one ServiceAccount are required")
	}

	if audience == "" {
		audience = DefaultAudience
	}

	hostPath := u.Host + u.Path
	op := "StringEquals"

	subs := make([]string, 0, len(serviceAccounts))

	for _, sa := range serviceAccounts {
		ns, name, ok := strings.Cut(sa, "/")
		if !ok || ns == "" || name == "" || strings.Contains(ns, "*") {
			return "", fmt.Errorf("oidcissuer: ServiceAccount %q is not <namespace>/<name>", sa)
		}

		if strings.Contains(name, "*") {
			op = "StringLike"
		}

		subs = append(subs, "system:serviceaccount:"+ns+":"+name)
	}

	cond := map[string]any{"StringEquals": map[string]any{hostPath + ":aud": audience}}
	if op == "StringLike" {
		cond["StringLike"] = map[string]any{hostPath + ":sub": subs}
	} else {
		cond["StringEquals"].(map[string]any)[hostPath+":sub"] = subs
	}

	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Effect":    "Allow",
			"Principal": map[string]any{"Federated": providerARN},
			"Action":    "sts:AssumeRoleWithWebIdentity",
			"Condition": cond,
		}},
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}

	return string(out), nil
}
