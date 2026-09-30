# Doctrine

This repository is held to the [component contract](https://github.com/truvity/policy/blob/master/docs/contracts/component.md);
this page says only what is particular to it and links there rather than
restating a rule.

## Provisioning is a component, not a recipe

A cluster is a dozen resources that only make sense together, and a repo that
copies them into each program drifts: one copy gets the fix, the next does
not. A `ComponentResource` with a documented set of children is the unit that
can be versioned, fixed once and adopted by preview.

## Names are the caller's

A component that chose a name would carry one estate's choice into the next,
and would collide the day two clusters of one estate shared it. Names,
ranges, tags and boundaries are inputs. The component's own children are
named after what they are, and those names are documented, because they are
what a URN is made of.

## One contract, providers behind it

What a consumer needs from a cluster is small and does not depend on who made
it. The contract states it once; a provider that cannot meet a capability
leaves it out rather than pretending. A consumer asks for a capability, never
for a provider.

## Adoption is proven by an empty preview

Extracting a component from a live estate is only safe if the estate's
resources do not move. The proof is mechanical: an empty preview per cluster,
the most important cluster last.

## Nothing here touches a real cloud in CI

Public CI is a mock for the provider layer and kind for the cluster layer.
A test that needs an account is a test that holds a credential, and a public
repository should hold none.
