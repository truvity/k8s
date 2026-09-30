// Package pullthroughcache deploys Amazon ECR pull-through cache rules for
// upstream container registries, as one Pulumi ComponentResource.
//
// A pull-through cache rule maps a local repository prefix to an upstream
// registry: the first pull of <account>.dkr.ecr.<region>.amazonaws.com/<prefix>/<image>
// lands the image in a local repository, and later pulls are same-region and
// free of the upstream's per-address rate limits.
//
// AWS constraints the component encodes:
//   - Some upstreams require credentials even for public images. They live in
//     a Secrets Manager secret whose name MUST start with
//     "ecr-pullthroughcache/", in the account and region of the rule, under
//     the default aws/secretsmanager key (no customer key).
//   - The secret payload is exactly {"username": ..., "accessToken": ...}.
//   - ECR validates the credential when the rule is created and treats a
//     secret with no version yet as not found, so a rule depends on both the
//     secret and its version.
//
// Credentials are inputs: the caller reads them from wherever it keeps
// them. The component never reads a parameter store or a vault.
//
// Principals that pull also need ecr:BatchImportUpstreamImage and
// ecr:CreateRepository on the prefix repositories. That grant belongs where
// the puller's role is defined; the component reports the prefixes so the
// caller can write it.
//
// See docs/reference.md for the children, their names and their aliases.
package pullthroughcache
