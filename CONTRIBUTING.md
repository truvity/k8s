# Contributing

Thanks for looking. This repository holds Pulumi components that provision
Kubernetes clusters, and the provider-neutral contract they report against.

## Before you open a pull request

```sh
devbox shell
just check      # build, test, lint, leak-canary
```

`just check` is exactly what CI runs on a pull request. `just vuln` runs on
a schedule in its own workflow.

## What belongs here

Mechanism. A component takes the cluster's name, its address ranges, its
permission boundaries and its tag patterns as inputs; it does not know any
particular estate. If a value would be different in another organisation,
it is an input. `hack/leak-canary.sh` catches the mechanical cases; review
catches the rest.

## Rules of the road

- A component's child names are API. Changing one is a `Breaking:` entry in
  the CHANGELOG and needs an alias; see
  [docs/decisions/0002-urn-stability.md](docs/decisions/0002-urn-stability.md).
- A new provider implements the contract in `pkg/cluster` and declares which
  capabilities it offers. It does not extend the contract for itself; a new
  capability is its own decision under `docs/decisions/`.
- Tests use Pulumi mocks and kind. A test that needs a real cloud account
  does not belong in this repository.
- Commits are small and say why. The default branch is `master`; pull
  requests merge by rebase.

## Reporting a vulnerability

Privately, as described in [SECURITY.md](SECURITY.md). Not as an issue.
