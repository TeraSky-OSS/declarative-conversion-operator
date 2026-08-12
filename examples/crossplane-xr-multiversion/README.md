# Multi-version Crossplane XR migration

The realistic case: `xwidgets.example.org` has been through two API revisions
and still serves all three versions. `v3` is the hub; `v2` and `v1` are spokes
that each drifted from it differently.

| File | What it is |
|---|---|
| `xrd.yaml` | Three served versions, `v3` referenceable. |
| `xrdconversionconfig.yaml` | Four rules for the `v2` spoke, three for `v1`. |
| `samples/` | One object at each of the three versions. |

## The story each spoke tells

**`v2`** is a cosmetic revision — same information, different shapes:

| Hub (`v3`) | `v2` | Rule |
|---|---|---|
| `spec.storageGB` | `spec.storageSize` | `FieldRename` |
| `spec.replicaCount` (integer) | `spec.replicas.count` | `ScalarToObject` |
| `spec.size: Large` | `spec.size: L` | `EnumRemap` |
| `status.phase` | `status.state` | `FieldRename` |

That last one is the point worth remembering: rules apply to `status.*` exactly
as they do to `spec.*`. A conversion webhook receives the whole stored object,
so `status` needs mapping too — and `spec.description`/`spec.zones`, identical
on both versions, need no rule at all.

**`v1`** predates two features, so it needs decisions rather than renames:

| Hub (`v3`) | `v1` | Rule |
|---|---|---|
| `spec.zones` (array, `maxItems: 1`) | `spec.zone` (object) | `SingletonArrayToObject` |
| `spec.description` | *(nothing)* | `ToAnnotation` with `restoreOnReverse: true` |
| *(nothing)* | `spec.debugMode` | `Delete`, `acknowledgeLossy: true` |

`ToAnnotation` stashes the hub's `description` into
`xrd.example.org/description` on the way down and reads it back on the way up,
which is what keeps it lossless — `samples/widget-v1.yaml` carries that
annotation (JSON-quoted, the default serialization) to show what a `v1` object
that has already been through the hub looks like.

`spec.debugMode` is the one genuinely lossy field here, and the config says so
on the record. Only `acknowledgeLossy: true` plus a `reason` gets a config with
a rule like this accepted at all.

## Run it

```console
convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl test     --config xrdconversionconfig.yaml --xrd xrd.yaml --samples ./samples/
```

Nine paths are tested — three samples across three served versions. Seven pass
outright; the two involving `samples/widget-v1.yaml` converting *up* report
`LOSS(acknowledged)` on `spec.debugMode`, and the run still exits `0`.
Acknowledged loss never fails the gate; unacknowledged loss is what
`convctl test` is there to catch.

Note that `v1→v2` in the report matches rules from both spokes. Spoke-to-spoke
conversions always route through the hub — two conversions, never a direct
path — exactly as they do in a live cluster.

## Reference

- [Field Rename](https://terasky-oss.github.io/declarative-conversion-operator/strategies/field-rename/) ·
  [Scalar ⇄ Object](https://terasky-oss.github.io/declarative-conversion-operator/strategies/scalar-object/) ·
  [Enum Remap](https://terasky-oss.github.io/declarative-conversion-operator/strategies/enum-remap/) ·
  [Singleton Array ⇄ Object](https://terasky-oss.github.io/declarative-conversion-operator/strategies/singleton-array-object/) ·
  [To Annotation / Label](https://terasky-oss.github.io/declarative-conversion-operator/strategies/metadata-stash/) ·
  [Delete](https://terasky-oss.github.io/declarative-conversion-operator/strategies/delete/)
- [Getting Started](https://terasky-oss.github.io/declarative-conversion-operator/getting-started/) — applying a config like this to a real cluster.
