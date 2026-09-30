# 0002 — Extraction keeps resource URNs stable

**Status:** Accepted

## Context

The components are extracted from infrastructure that is running. Pulumi
identifies a resource by its URN, which includes its parent chain and name.
Wrapping loose resources in a `ComponentResource` changes every child's
parent, so every child's URN, and an unaware program would plan to delete and
recreate a cluster's network and control plane.

## Decision

1. **Components are `ComponentResource`s with documented children.** Each
   component's page lists its children and their names. The names are API.
2. **Aliases carry the old URNs.** Every child declares an alias from the URN
   it had before it was wrapped. The caller supplies whatever the alias needs
   that the component cannot know (the old parent, the old name prefix).
3. **The caller's URN golden is the contract.** The estate being migrated
   records the URNs of the stacks it adopts in a golden file under test; the
   extraction is correct when that golden does not change.
4. **An empty preview per cluster, before any apply.** Each cluster is
   adopted on its own: write, preview, expect nothing, apply (a rename of
   state), refresh-preview, expect nothing.
5. **The most important cluster last.** The first adoptions are the ones
   where a wrong alias is cheap. The cluster the estate cannot lose is
   adopted after every alias shape has been proven on the others.
6. **Extract first, then change.** A component is lifted as it runs. Fixes
   and new inputs come in later releases, each under its own preview, so a
   non-empty preview is always the alias's fault and never a mixed change.

## Consequences

- A component cannot rename a child without an alias and a `Breaking:`
  entry.
- Tests here assert the URNs a component produces against a documented list,
  using Pulumi mocks, so a renamed child is a red test rather than a
  production preview.
- Adoption takes several reviewed steps. That is the price of never
  replacing a cluster by accident.
