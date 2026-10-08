# Value contracts

The `values.schema.json` of the charts below is generated from a Pkl contract in
[`contracts/`](../contracts) with the generators of
[truvity/pkl-contracts](https://github.com/truvity/pkl-contracts) v0.7.0. The
contract is the single source; the committed schema is its output, and CI fails
when the two differ.

| Chart | Contract |
|---|---|
| `cilium-config` | `contracts/cilium-config/Values.pkl` |
| `cluster-baseline` | `contracts/cluster-baseline/Values.pkl` |
| `cluster-foundation` | `contracts/cluster-foundation/Values.pkl` |
| `cluster-network-policies` | `contracts/cluster-network-policies/Values.pkl` |
| `eks-auto-node-pools` | `contracts/eks-auto-node-pools/Values.pkl` |
| `guardrails-projects` | `contracts/guardrails-projects/Values.pkl` |
| `tenancy` | `contracts/tenancy/Values.pkl` |

`cilium-crds` and `volume-snapshot-crds` are not here: their (empty) schema
belongs to the upstream CRD mirror.

A chart whose schema says something v0.7.0 cannot is left as it was, whole: the
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
ship in every `.tgz`, and the shared types (`Common.pkl`: the string map, object and
annotation names, Pod Security levels) want one lock file and one set of pins.

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
| `propertyNames` with a pattern, `not` pattern (`system-`), length | the key alias's `@A.Pattern`, `@A.NotPattern`, `@A.Length` |
| a typed object that stays open (BGP specs) | `@A.Open` |
| `oneOf [{const: false}, namespace]` | `(V.FalseOnly \| Namespace)?` |
| `if deny contains egress then require dnsEgress else forbid it` | `@A.RequiredWhen { contains }` with `@A.ForbiddenUnless { contains }` |
| `anyOf [{required: [reason]}, {required: [owner]}]` | `@A.RequiredAnyOf` |
| a namespace entry whose `level` differs from the default needs `reason` | `@A.RequiredWhenDiffers` |
| a closed object whose optional keys share one value type, with `minProperties: 1` (`limitRange`) | `Mapping<"default" \| ..., QuantityMap>` and `@A.Properties` |
| a namespace name | `V.DnsLabel` (the same language; a key's `@A.Length` now renders) |

## Parity with the hand-written schemas

No behaviour changes except the line-break guard below. The reference is the
hand-written schemas of `master` before this change. How it was shown, for all
seven charts:

1. **Structure.** Both schemas with every `$ref` inlined, `description`, `title`
   and the guard left out, compared apart; the differences left are the ones in
   the table below.
2. **Fixtures.** Every golden case (`tests/cases/*`: 29 `helm template` renders,
   byte-identical), every negative fixture (`tests/invalid/*`: 166 files, all
   still refused, 92 of them by the schema alone), and the `bogusKey` and
   `helm lint` checks of `just lint` give the same result with the old and the
   new schema.
3. **Differential run.** The Helm validation engine (santhosh-tekuri/jsonschema
   v6) asked both schemas about 4,020,821 documents: every fixture, every
   fixture merged over the chart's defaults, and each of those with every value
   replaced in turn by about 200 probes (wrong types, `null`, empty, boundary
   numbers, pattern near-misses, line breaks, lists with a duplicate, whole maps
   and lists), an unknown key, the key removed, and a new map key from a list of
   valid and invalid names. Zero verdict differences except 216 documents, all
   the line-break guard below. The run flags a schema with a pattern widened by
   one or a `minItems` dropped, so it can see a difference.

| Chart | Goldens | Negatives (by schema alone) | Documents | Differences | Guard only |
|---|---|---|---|---|---|
| `cilium-config` | 3 | 14 (9) | 231,512 | 0 | 0 |
| `cluster-baseline` | 9 | 37 (19) | 1,468,378 | 0 | 160 |
| `cluster-foundation` | 4 | 22 (12) | 321,922 | 0 | 0 |
| `cluster-network-policies` | 3 | 13 (9) | 203,840 | 0 | 0 |
| `eks-auto-node-pools` | 3 | 19 (10) | 308,170 | 0 | 0 |
| `guardrails-projects` | 2 | 8 (1) | 233,348 | 0 | 24 |
| `tenancy` | 5 | 53 (32) | 1,253,651 | 0 | 32 |

### What changed in each file, and why it does not matter

| Change | Where | Why behaviour is the same |
|---|---|---|
| `$schema` draft-07 to 2020-12; `definitions` to `$defs` | all | the keywords in use mean the same in both; no `$ref` has a sibling keyword |
| `oneOf` to `anyOf` | quantities, `protect`, `namespace` | the members are disjoint (a string and a number; a boolean and a string; `false` and an object) |
| `const: "prune-only"` becomes `enum: ["prune-only"]` | `tenancy` | one member |
| a string `enum` loses `type: string`; `propertyNames` loses `type: string` | all | a member or a name is always a string |
| `items: {}` stated | free-form lists | the same as no `items` |
| `limitRange`/`container`: `additionalProperties: false` + four typed properties becomes `propertyNames: {enum}` + one `additionalProperties` | `guardrails-projects`, `cluster-baseline` | the four properties have the same schema |
| a namespace name: `{0,61}` in the pattern becomes `*` and `maxLength: 63` (`V.DnsLabel`) | wherever a namespace name is | the same language, 1 to 63 characters |
| `cilium-config` `block`: `oneOf` of `required` clauses becomes `anyOf` of two closed classes (`cidr`, or `start` + `stop`) | `cilium-config` | each class refuses the other's keys, so exactly the old pairs are accepted |
| the PSA conditional (three pasted `if`/`then`) becomes one rule per level; `contains`/`else` becomes `allOf` of `if`/`then` | `cluster-baseline` | checked by the 160-case rule matrix and the differential run |
| principal `minLength: 1` dropped | `cluster-baseline` `labelGuard` | the pattern needs at least one character |
| `rules` description moved from the definition to its properties | `cluster-network-policies` | a description is not validation |
| a line-break guard (`not: { pattern: <line breaks> }`) beside each `pattern` | every patterned string | see below |
| `$defs` order, property order | all | JSON objects are unordered |

### The line-break guard

The generator refuses a line break (CR, FF, VT, NEL, U+2028, U+2029, and LF) in
every patterned string. Where the hand-written pattern admitted one, the
contract now refuses it. The differential run found these fields, none of which
can legitimately hold a line break (an IAM role ARN, a user, group or user prefix
of the label guard):

- `ackRoleSelectors.namespaces.<name>.roleARN` (`cluster-baseline`)
- `labelGuard.allowedUsers[]`, `allowedUserPrefixes[]`, `allowedGroups[]` (`cluster-baseline`)
- `projects[].ackRoleARN`, `podIdentities.associations.<name>.roleARN` (`guardrails-projects`)
- `podIdentities.associations.<name>.roleARN` (`tenancy`)

No field needed `@A.MultiLine`.

## Differences from the vocabulary, kept on purpose

The contract keeps today's rule wherever the vocabulary differs. Each is a
follow-up: change the rule, deliberately, in a PR of its own.

- Object names are local patterns: 1 to 253 characters, dotted, stricter than
  `DnsName` and looser than `DnsSubdomain` (`guardrails-projects` also admits a
  colon).
- The annotation key, label key and prefix, domain, role ARN, NATS URL, CIDR,
  Pod Security `version` (`""` is a member), the ACK name prefix (`""` is a
  member) and quantities have local patterns; none equals a vocabulary type.
  Quantities are a string or, in some places, a whole number.
- `V.KarpenterDuration`, `V.NonEmptyString`, `V.DnsLabel`: the same rule as
  today's; used as is.
- `syncWave` and `protect` admit `null`; the vocabulary never does.
- `""` is a member of several enums and patterns (`podSecurity.level`,
  `version`, name prefixes), and `values.yaml` defaults those strings to `""`
  where the vocabulary says "absent".
- `limitRange` is an enum-keyed map in the contract.
