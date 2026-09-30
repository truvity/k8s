# 0003 — A component carries its own adoption alias

**Status:** Accepted

## Context

[0002](0002-urn-stability.md) says every child declares an alias from the URN
it had before it was wrapped, and that the caller supplies what the alias
needs. For the first extracted component, the pull-through cache, the old
shape is the same for every child: a resource registered directly under the
stack, with the name it still has. There is nothing for the caller to
compute, and a caller that hand-writes the alias for every child is one typo
from a replace.

## Decision

1. **One switch, not a list of aliases.** A component whose old shape is
   "loose under the stack" takes a boolean (`LegacyTopLevel`). When set,
   every child gets `Aliases: [{NoParent: true}]`: the current name and type,
   no parent. Left false, a new deployment carries no alias.
2. **The name in the alias is the name in use.** The component takes an
   optional naming hook; the alias follows it, because the alias never
   carries a name of its own. A caller whose resources were created under
   other names passes a hook that reproduces them.
3. **Only children are aliased.** The provider resource, and anything the
   caller registers itself, stays where the caller put it.
4. **The tests pin the set.** A component's test asserts, under Pulumi mocks,
   the children's types and names, their parent being the component, and the
   alias on each. A renamed child or a lost alias is a red test.
5. **A component with a different old shape** (an old parent, a renamed
   prefix) takes an input that carries that shape instead, and documents it
   on its reference page. The switch is for the top-level case only.

## Consequences

- Adoption is one line for the caller and one golden diff: a new component
  entry, the children's parent moved to it, and an alias on each child.
  Nothing is added or removed.
- The alias is permanent until the caller has applied once. Afterwards
  `LegacyTopLevel` can be dropped: state holds the new URNs and the alias no
  longer matches anything.
- A component's children names are API (0002); the naming hook is how a
  caller keeps names that were chosen before the component existed, and it
  is refused when it returns an empty or repeated name.
