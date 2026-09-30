# Adoption

## Using the contract

```sh
go get github.com/truvity/k8s@v0.1.0
```

`pkg/cluster` has no cloud dependency. A provider component reports a
`cluster.Outputs`; a consumer calls `Validate` on it once, at the boundary,
and then asks `Capabilities.Has(...)` for what it needs.

## Adopting a component over existing infrastructure

A component is adopted cluster by cluster, never all at once, and each
adoption follows the same four steps.

1. **Write the program.** Replace the loose resources with the component,
   passing the names the resources already have. The names are inputs so
   that the adopted resources keep the physical names they were created
   with.
2. **Declare the aliases.** Each component documents its children and the
   URN each child had when it was a loose resource; the aliases it declares
   map the old URN to the new one. You supply the old parent (your stack's
   root, or your previous component) where the alias needs it.
3. **Preview.** The preview must be empty: no create, no delete, no replace,
   no update. Anything else means a name, an alias or an input is wrong;
   fix that, never apply "just to see".
4. **Apply, then refresh-preview.** The apply only renames state. A
   refresh-preview afterwards must still plan nothing.

Order the clusters so that the one that matters most comes last: a wrong
alias is found on the cluster you can afford to learn on.

## Upgrading

Breaking changes are `Breaking:` bullets in the [CHANGELOG](../CHANGELOG.md)
and, for a renamed child, come with the alias that keeps the old URN. Pin an
exact version and read the bullets between the old pin and the new.
