# 0005 — Pull isolation is a per-namespace image allow-list, observed first

**Status:** Accepted.

## Context

A cluster has one node role, and it pulls every image. Where a registry sits
therefore says nothing about which namespace may run what: a pod in one project
can name another project's image and the node will pull it. The boundary has to
be drawn where the pod is admitted, by namespace.

## Decision

1. **A list of registry prefixes per namespace, enforced by admission.** The
   `imageAllowList` policy in `guardrails-projects` is built from the project
   rows: a row's list is the shared `common` entries, the `sets` it names and
   its own `allow` entries. It covers every way a pod names an image:
   containers, init containers, ephemeral containers and image volumes.
2. **An entry is a path prefix or an exact repository, nothing looser.** A
   trailing `/` is a prefix; without one the entry is that repository, with any
   tag or digest. No wildcard, no tag, no digest in an entry: a rule that can
   be read two ways is read the wrong way by someone. An image that does not
   begin with a registry host never matches.
3. **Observe, then enforce, one namespace at a time.** The binding defaults to
   `[Warn, Audit]`. A warning on a pod a controller creates is seen by nobody, so
   the audit annotation that lists every offender is the signal. `[Deny, Audit]`
   enforces, for every namespace, and `images.enforce` on a row enforces that
   namespace alone through a second binding of the same policy (`<name>-enforce`)
   that selects the enforcing rows while the main binding selects the rest. A
   cluster-wide switch would turn every row on at once, and the rows differ in
   how quiet their audit log is.
4. **A namespace with no list is not checked, and the render keeps that
   honest.** Unlike a PodIdentityAssociation, a pod exists in every namespace;
   default deny would stop the platform. Instead `kinds` names the rows that must
   carry a list, and a row that cannot yet has `exempt` with a reason: a debt
   that is written down.

## Not chosen

- **Default deny for a namespace with no row.** It would refuse the cluster's
  own pods; the render check and the exemption reach the same safety without it.
- **Excluding ephemeral containers.** It leaves one unchecked way to pull. A
  team that debugs lists its debug image in `common`.
- **Digest-pinned entries.** The registry side already keeps tags immutable.
  Revisit if a registry cannot.
- **One policy per enforcing row, or an `enforce` flag read by the expression.**
  The first multiplies the cost-bounded map; the second makes the verdict depend
  on a value the expression carries, while a binding's selector is the mechanism
  the API server already offers for "this namespace, this action".
- **A stricter `kinds` default.** `kinds` is empty so a cluster that does not
  set it sees no change; a consumer that wants every project row covered sets it.
