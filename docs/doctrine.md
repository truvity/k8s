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

## A baseline labels namespaces, it does not own them

A namespace has an owner: whoever created it, a tool that prunes it, a chart
that names it. A second tool that renders the whole `Namespace` object
fights the first over every field and, on removal, deletes the namespace and
everything in it. `cluster-baseline` therefore renders labels-only objects
for server-side apply, where the field manager owns exactly the PSA labels,
and marks each object so no tool deletes it when a name leaves the list. A
Job that runs `kubectl label` would also work, and was refused: it needs an
image (an estate fact), a credential to patch every namespace, and it leaves
no declared state to review or to drift against.

## Warn first, enforce last

A refusal is a loud, late failure: a deployment stops rolling in the middle
of an unrelated change. A warning is the same finding with no outage. The
defaults therefore warn and audit and never enforce, and enforcement is a
reviewed values change that can be made one namespace at a time.

## An exemption is a reasoned debt

A namespace that cannot meet the default level is listed with a lower one and
a reason, and the render refuses it without the reason. The reason is also
written to the namespace as an annotation, so it is read where the exemption
is felt. A quota is the same kind of debt: it can stop a workload, so it
carries a `reason` or an `owner`. An exemption is never a silent default: no namespace is exempt unless
someone said so. It names the cause (host namespaces, privileged storage
driver, a build daemon) and is deleted when the cause is.

## The chart carries no namespace names

Which namespaces exist, and which are exempt, is the estate's. The list is
empty by default and the schema accepts any DNS label.

## No floor, and fix rather than exempt

There is no cluster-wide baseline floor: a floor admits almost everything, so
it buys little, and it is a second target that must later be raised.
Namespaces move to `restricted` one at a time. A workload that fails
`restricted` is fixed (that is the policy, and the workload owner's change);
an exemption is the last resort.

## A guard warns; it does not own

The guard only validates, in Warn and Audit by default, and fails open. It
asks namespaces to carry a label and never writes one, so no owner is fought.

## Extras are opt-in and name their owners

A default-deny policy, a quota and a limit range change what workloads can do,
so each renders nothing until a namespace opts in, ships no numbers, and says
how to remove it. Each decides deliberately whether removal is harmful:
all three are prunable by default, because removing one only relaxes a limit
or hands the namespace to its owner, and each has a `protect` switch. A deny
policy never duplicates one an owner already ships.
