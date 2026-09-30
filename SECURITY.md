# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it privately via
[GitHub Security Advisories](https://github.com/truvity/k8s/security/advisories/new).

Do NOT open a public issue for security vulnerabilities.

## Supported Versions

Only the latest release is supported with security updates.

| Version | Supported |
|---------|-----------|
| latest  | yes       |
| older   | no        |

## Design notes relevant to a reviewer

- This module provisions infrastructure, so what it *defaults* is a
  security surface. It ships no default that widens access: API endpoint
  exposure, peering routes, registry and bucket policies are inputs, and an
  input left empty means nothing is opened.
- It holds no credentials. Cloud credentials belong to the Pulumi program
  that calls it; the module reads none and writes none into state beyond
  what a resource's own outputs carry. Secret outputs stay Pulumi secrets.
- The contract validator (`pkg/cluster`) refuses a plain-http endpoint and
  an OIDC issuer that is not https, because a consumer that trusts either
  would send credentials in the clear.
- Pod Security `restricted` is not in this module; it ships later as its
  own chart, and until it does a cluster's workloads are as permissive as
  the provider's default.
- Tests never touch a real cloud, so no test credential exists to leak.
