# Value contracts

The `values.schema.json` of the charts below is generated from a Pkl contract in
[`contracts/`](../contracts) with the generators of
[truvity/pkl-contracts](https://github.com/truvity/pkl-contracts) v0.6.0. The
contract is the single source; the committed schema is its output, and CI fails
when the two differ.

| Chart | Schema |
|---|---|
| `cluster-network-policies` | generated from `contracts/cluster-network-policies/Values.pkl` |
| `eks-auto-node-pools` | generated from `contracts/eks-auto-node-pools/Values.pkl` |
| `guardrails-projects` | generated from `contracts/guardrails-projects/Values.pkl` |
| `cilium-config` | hand-written (see "Not converted yet") |
| `cluster-baseline` | hand-written |
| `cluster-foundation` | hand-written |
| `tenancy` | hand-written |

`cilium-crds` and `volume-snapshot-crds` are not here: their (empty) schema
belongs to the upstream CRD mirror.

A chart whose schema says something v0.6.0 cannot is left as it was, whole: the
generated output is never patched, and `contract-check` covers only the
converted charts.

## Working with a contract

```console
$ just contract         # regenerate the converted charts' values.schema.json
$ just contract-check   # what CI runs: regenerate aside, fail on any difference
```

Edit the contract, run `just contract`, commit both. Never edit a generated
`values.schema.json` by hand. `values.yaml` (the defaults) is still written by
hand; moving it into the contracts is a follow-up.

Pkl is run as `hack/pkl`, a pinned wrapper (Pkl 0.32.1, sha256-checked; the same
as `bin/pkl` of pkl-contracts) until nixpkgs ships Pkl 0.32. The contract
packages are pinned by checksum in `contracts/PklProject.deps.json`.

## Layout

One Pkl project at the repository root, not a `contract/` directory in each
chart: Helm packages a chart's directory, so Pkl sources inside a chart would
ship in every `.tgz`, and the shared types (`Common.pkl`: the string map and the
object name) want one lock file and one set of pins.

```
contracts/
  PklProject, PklProject.deps.json   the four pkl-contracts packages, pinned
  Common.pkl                         types shared between charts
  Generate.pkl                       the command: one chart's contract -> values.schema.json
  <chart>/Values.pkl                 the contract of one chart
```

A contract is a module annotated `@A.Chart`; its doc comment is the schema's
`description`, and a property's doc comment is that property's `description`. A
property that is not nullable is `required`; `X?` is optional. A required list,
map or object is written `X?` with `@A.Required` (Pkl gives a `Listing`, a
`Mapping` and a class of optional fields an implicit default, so a bare
`Listing<X>` would be optional). A string with a rule is a local `typealias`
carrying the hand-written pattern. `@A.Def` makes an alias or a class a
`$defs` entry reached by `$ref`. Counts are annotations: `@A.Items { min;
unique }`, `@A.Properties`, `@A.Range`, `@A.Length`.

## Declared, not patched

| Today's rule | Annotation / type |
|---|---|
| `type: [integer, null]`, `type: [boolean, null]` (`syncWave`, `protect`) | `@A.Nullable` |
| `minItems`, `uniqueItems` | `@A.Items` |
| `minProperties` on a map | `@A.Properties` |
| `minimum`, `maximum`, `minLength` | `@A.Range`, `@A.Length`, `V.NonEmptyString` |
| `definitions` + `$ref` | `@A.Def` |
| a required list, map or object, no default | `@A.Required` |
| a closed object whose four optional keys share one value type, with `minProperties: 1` (`limitRange`) | `Mapping<"default" \| ... , QuantityMap>` and `@A.Properties`, see below |

## Parity with the hand-written schemas

No behaviour changes except the one tightening below. How that was shown, for
the three converted charts:

1. **Structure.** Both schemas with every `$ref` inlined, `description`, `title`
   and the line-break guard left out, compared apart. The differences left are
   the ones listed in the next section.
2. **Fixtures.** Every golden case (`tests/cases/*`, 8 `helm template` renders:
   byte-identical output), every negative fixture of the three charts
   (`tests/invalid/*`, 40 files: all still refused, 20 of them by the schema
   alone) and the `bogusKey` and `helm lint` checks of `just lint` give the same
   result with the old and the new schema.
3. **Differential run.** The Helm validation engine (santhosh-tekuri/jsonschema
   v6) asked both schemas about 745,358 documents: every fixture, every
   fixture merged over the chart's defaults, and each of those with every value
   replaced in turn by about 200 probes (wrong types, `null`, empty, boundary
   numbers, pattern near-misses, line breaks, lists with a duplicate, whole maps
   and lists), an unknown key, the key removed, and a new map key from a list of
   valid and invalid names. Everything but one kind of verdict is the same: 24
   documents differ, all of them the line-break guard below. The same run
   flags a schema with a pattern widened by one or a `minItems` dropped, so it
   can see a difference.

### What changed in each file, and why it does not matter

| Change | Where | Why behaviour is the same |
|---|---|---|
| `$schema` draft-07 to 2020-12 | all three | the keywords in use (`type`, `properties`, `required`, `additionalProperties`, `propertyNames`, `items` as one schema, `enum`, `pattern`, `minLength`, `minimum`/`maximum`, `minItems`, `uniqueItems`, `minProperties`, `$ref`, `anyOf`) mean the same in both; no `$ref` has a sibling keyword |
| `definitions` to `$defs`, `#/definitions/x` to `#/$defs/x` | all three | resolved against the document root |
| `oneOf` to `anyOf` | `guardrails-projects`: a quantity is a non-empty string or a number | the members are disjoint, so exactly-one and at-least-one accept the same documents |
| a string `enum` loses `type: string` | all three | every member is a string |
| `propertyNames` loses `type: string` | all three | a property name is always a string |
| `items: {}` stated | `guardrails-projects`: `from`, `to`, `ports` | the same as no `items` |
| `limitRange`: `additionalProperties: false` + four typed properties becomes `propertyNames: {enum}` + one `additionalProperties` | `guardrails-projects` | the four properties have the same schema, so the closed object and the enum-keyed map accept the same documents |
| a line-break guard (`not: { pattern: <line breaks> }`) beside each `pattern` | every patterned string | see "The one tightening" |
| `$defs` order, property order; an unused `annotationKey` definition | all | JSON objects are unordered; the key pattern is inlined as well |
| `rules` description moved from the definition to `ingress` and `egress` | `cluster-network-policies` | a description is not validation |

### The one tightening

The generator refuses a line break (CR, FF, VT, NEL, U+2028, U+2029, and LF) in
every patterned string. The differential run found two fields where the
hand-written pattern admitted one, both the IAM role ARN
`^arn:[a-z-]+:iam::[^:]*:role/.+$` (`[^:]*` matches a line break, and `.`
matches CR, NEL and U+2028):

- `guardrails-projects`: `projects[].ackRoleARN`
- `guardrails-projects`: `podIdentities.associations.<name>.roleARN`

A line break in an ARN is invalid anyway. No field needed `@A.MultiLine`.

## Differences from the vocabulary, kept on purpose

The contract keeps today's rule wherever the vocabulary differs. Each is a
follow-up: change the rule, deliberately, in a PR of its own.

- Names are local patterns. The namespace name (`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
  admits the same strings as `DnsLabel`, but says the bound in the expression;
  a map key keeps only a `pattern` (the generator writes a key's `@A.Length`
  nowhere), so `DnsLabel` as a key would drop the 63-character bound. The object
  name is a 1 to 253 character dotted name (stricter than `DnsName`, looser than
  `DnsSubdomain`), and `guardrails-projects` admits a colon in it.
- The annotation key, the Pod Security `version` (`""` is a member), the role ARN
  and the ACK name prefix (`""` is a member) have no vocabulary type.
- A quantity is a non-empty string or a number, not the vocabulary's `Quantity`
  (a string with a unit).
- `V.KarpenterDuration` (`eks-auto-node-pools`), `V.NonEmptyString`: the same
  expression as today's; used as is.
- `syncWave` and `protect` admit `null`; the vocabulary never does.
- `""` is a member of the `version` pattern and of the name prefix, and
  `values.yaml` defaults those strings to `""` where the vocabulary says
  "absent".
- `limitRange` is an enum-keyed map in the contract (no annotation counts the
  properties of a class).

## Not converted yet

Four charts keep their hand-written schema, each for a rule v0.6.0 has no
annotation for. The missing feature is named so that pkl-contracts can add it.

### `cilium-config`: an open typed object

`bgpClusterConfig.spec`, the instance and peer items under it, and
`bgpAdvertisement.spec` are objects with typed or required properties that stay
open (no `additionalProperties: false`), because the chart renders the Cilium
CRD's spec verbatim:

```json
"spec": { "type": "object", "required": ["bgpInstances"],
  "properties": { "bgpInstances": { "type": "array", "minItems": 1,
    "items": { "type": "object", "required": ["name", "localASN"],
      "properties": { "name": { "type": "string", "minLength": 1 },
                      "localASN": { "type": "integer", "minimum": 1, "maximum": 4294967295 } } } } } }
```

A nested class is always closed (`additionalProperties: false`); only a
`@Schema` document class can be `open`. Needed: an annotation or modifier that
leaves a nested class open. (The block's `oneOf` of `cidr` or `start`+`stop` is
expressible as a union of two closed classes.)

### `cluster-baseline`: rules across fields

1. A condition through a map, on a value that is not one: for every namespace
   under `podSecurity.namespaces`, a `level` different from the default
   `level` requires `reason`:

   ```json
   "if": { "properties": { "level": { "const": "privileged" } }, "required": ["level"] },
   "then": { "properties": { "namespaces": { "additionalProperties": {
     "if": { "properties": { "level": { "not": { "const": "privileged" } } }, "required": ["level"] },
     "then": { "required": ["reason"] } } } } }
   ```

   `@RequiredWhen` takes a dotted path through blocks only (never a map) and its
   condition is "is one of", never "is not".
2. A condition on an array that contains a value, with the other branch
   forbidding the key: `networkPolicyNamespace`
   `if deny contains "egress" then required [dnsEgress] else not required [dnsEgress]`.
3. At least one of two optional keys: `anyOf: [{required: [reason]}, {required: [owner]}]`
   (`quotaNamespace`, `limitRangeNamespace`).

### `cluster-foundation`: a pattern a key must not match

```json
"priorityClasses": { "type": "object",
  "propertyNames": { "allOf": [ { "$ref": "#/definitions/objectNamePattern" },
                                { "not": { "pattern": "^system-" } } ] } }
```

`@NotPattern` on a `Mapping` key alias is accepted by the model and then not
written: the generated `propertyNames` has the `pattern` and no `not`, so the
contract would admit `system-x`. `@DenyKeys` refuses exact names, not a prefix.

### `tenancy`: a boolean constant

```json
"namespace": { "oneOf": [ { "const": false }, { "$ref": "#/definitions/namespace" } ] }
```

Pkl has no boolean literal type (`false | Namespace` does not parse) and
v0.6.0 has no `const` annotation; `Boolean | Namespace` would admit `true`.
