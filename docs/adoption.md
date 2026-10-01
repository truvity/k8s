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

## cluster-baseline: warn first

`cluster-baseline` puts Pod Security Admission labels on the namespaces you
list. PSA cannot be configured cluster-wide on a managed control plane, so
labels per namespace are the mechanism, and a namespace you do not list is
not covered. The rollout is three stages, each its own reviewed values
change, per cluster, the least important cluster first.

1. **Warn and audit (the defaults).** List the namespaces, pin nothing yet.
   Every listed namespace is warned and audited at `restricted`; nothing is
   refused. Applying a workload now prints a warning naming each violated
   control, and the audit log records it. Before listing a namespace, dry-run
   the stricter label to see what would break:

   ```sh
   kubectl label --dry-run=server --overwrite ns <ns> \
     pod-security.kubernetes.io/enforce=restricted
   ```

   The API server answers with a warning per distinct violation set. It
   checks the pods that exist now, so a workload scaled to zero shows
   nothing; read the pod templates of those.
2. **Settle exemptions.** For each namespace that cannot meet `restricted`,
   either fix the workload (a `seccompProfile`, no privilege escalation,
   dropped capabilities, a non-root user) or give the namespace the level it
   can meet with a `reason`. Expect a short list: node-level agents, storage
   and CSI drivers, container-build daemons and CI runners are the usual
   ones. Pin `podSecurity.version` to the cluster's Kubernetes minor before
   the next stage.
3. **Enforce, one namespace at a time.** There is no cluster-wide baseline
   floor: do not switch `podSecurity.modes.enforce.enabled` on for the whole
   cluster, and do not enforce `baseline` everywhere as an interim. Pin the
   version first, then give a namespace a `modes.enforce` override (the
   canary) and, once it has been quiet, move that namespace to `restricted`.
   The next namespace follows, each its own reviewed change. The policy is to
   fix a workload that fails `restricted` rather than exempt it; an exemption
   is `privileged` plus a `reason`, for the few that cannot.

The **guard** (`guard.enabled`) belongs in stage 1, in Warn mode: it warns and
audits when a namespace is created without a label, which tells you which
namespaces the list does not cover yet. Keep `validationActions: [Warn, Audit]`
and `failurePolicy: Ignore`; add `Deny` only when the warnings are quiet and
after reviewing `excludeNamespaces`.

The opt-in extras (`networkPolicy`, `resourceQuota`, `limitRange`) are not part
of this rollout and change nothing until a namespace opts in. Adopt them
separately, one namespace per change: check first that the namespace has no
default-deny policy of another owner, and give every quota a `reason` or an
`owner`.

Rolling back is a values change: switch the kind off or lower the level. A
refused pod is a deployment that stops rolling, so enforce on a day someone
is watching.

Apply the chart server-side (see the [reference](reference.md#chartscluster-baseline)).
Client-side apply makes the chart the owner of the whole namespace object.

## Upgrading

Breaking changes are `Breaking:` bullets in the [CHANGELOG](../CHANGELOG.md)
and, for a renamed child, come with the alias that keeps the old URN. Pin an
exact version and read the bullets between the old pin and the new.
