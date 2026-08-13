# Kitchen sink: every strategy at once

[`internal/cli/testdata/full/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/internal/cli/testdata/full)
is the one place in the repository where **all 29 built-in strategies** are
exercised against a single schema: a three-version XRD with a hub and two
spokes, 30 rules covering all 29 strategies, plus sample objects at every
version. It is the
fixture the CLI's own end-to-end tests and the e2e suite run against, so it is
correct by construction — if a strategy's YAML shape ever changed, this fixture
would break first.

It is a *reference*, not a tutorial. Read it when you want to see a strategy's
exact YAML in the context of a whole config rather than in isolation. If you
want a config to copy and adapt, start with the [examples
gallery](index.md) instead — those are written to be read top to bottom.

## What's in it

| File | What it is |
|---|---|
| [`xrd.yaml`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/internal/cli/testdata/full/xrd.yaml) | `xwidgets.example.org` with three served versions: `v3` (hub, `referenceable: true`), `v2`, and `v1`. |
| [`config.yaml`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/internal/cli/testdata/full/config.yaml) | The `XRDConversionConfig`: 14 rules for the `v2` spoke, 16 for `v1`, 30 in total covering 29 distinct strategies. |
| [`config-norules.yaml`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/internal/cli/testdata/full/config-norules.yaml) | The same config with every rule stripped — the "new spoke version, no mapping yet" starting point for `convctl suggest`. |
| [`samples/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/internal/cli/testdata/full/samples) | One object per version: `hub-v3.yaml`, `spoke-v2.yaml`, `spoke-v1.yaml`. |

## Run it

```console
git clone https://github.com/terasky-oss/declarative-conversion-operator
cd declarative-conversion-operator

go run ./cmd/convctl test \
  --xrd internal/cli/testdata/full/xrd.yaml \
  --config internal/cli/testdata/full/config.yaml \
  --samples internal/cli/testdata/full/samples/
```

With `convctl` already installed, drop the `go run ./cmd/` prefix. The run ends
with:

```text
SUMMARY: 3 samples, 9 paths — 3 PASS, 6 LOSS(acknowledged), 0 FAIL(unacknowledged loss), 0 ERROR
```

and exits `0`.

Nine paths is three samples across three served versions. The three `PASS`
results are the identity paths (`v3→v3`, `v2→v2`, `v1→v1`); the other six carry
acknowledged loss, because this fixture deliberately includes every strategy
that is *always* lossy in one direction — `delete`, `constant`, `defaultValue`,
`numericScale` onto an integer, `mapToArrayByKey`. Each of those rules states
its `reason` in the config, and acknowledged loss never fails a run at any
`--fail-on` threshold. What would fail is loss nobody declared.

Two more things the report shows that are easy to miss:

- **Spoke-to-spoke paths match rules from both spokes.** `v1→v2` lists `v2:`
  and `v1:` rules because every spoke-to-spoke conversion routes through the
  hub — two conversions, exactly as in a live cluster.
- **`RULE COVERAGE` lists all 30 rules with a match count.** A rule no sample
  exercised would show up here as a warning, which `--strict` (or
  `--fail-on warn`) escalates to a failure.

## Rule index

Where to find each strategy in
[`config.yaml`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/internal/cli/testdata/full/config.yaml).
Rule numbers are the indices the `convctl test` report prints
(`v2:rule[4]:FieldsToMap`), so a report line points straight at a block of YAML.

### `v2` spoke — 14 rules

| # | Strategy | What it maps here |
|---|---|---|
| 0 | [`FieldRename`](../strategies/field-rename.md) | `spec.storageGB` → `spec.storageSize` |
| 1 | [`FieldRename`](../strategies/field-rename.md) | `status.phase` → `status.state` — the same strategy on a `status` path |
| 2 | [`ScalarToObject`](../strategies/scalar-object.md) | `spec.replicaCount` → `spec.replicas.count` |
| 3 | [`ObjectToScalar`](../strategies/scalar-object.md) | `spec.network.cidr` → `spec.networkCIDR` |
| 4 | [`FieldsToMap`](../strategies/fields-map.md) | `spec.cpuLimit` + `spec.memoryLimit` → `spec.limits` map |
| 5 | [`ToAnnotation`](../strategies/metadata-stash.md) | `spec.description` → annotation, `restoreOnReverse: true` |
| 6 | [`FromAnnotation`](../strategies/metadata-stash.md) | hub annotation → `spec.operatorNote`, `stashOnReverse: true` |
| 7 | [`EnumRemap`](../strategies/enum-remap.md) | `spec.size`: `Small`/`Medium`/`Large` ⇄ `S`/`M`/`L` |
| 8 | [`Constant`](../strategies/constant.md) | forces `spec.schemaVersion: "v2"` on the spoke — always lossy |
| 9 | [`JSONPatch`](../strategies/json-patch.md) | escape hatch: `move` `spec.legacyFlag` ⇄ `spec.legacyFlagV2`, with `losslessOverride` |
| 10 | [`TypeCoerce`](../strategies/type-coerce.md) | `spec.priority`: integer on the hub, string on the spoke |
| 11 | [`ScalarToFields`](../strategies/scalar-fields.md) | `spec.diskSize: "50Gi"` → `spec.diskSizeValue` + `spec.diskSizeUnit` |
| 12 | [`NumericScale`](../strategies/numeric-scale.md) | `spec.memoryMB` ⇄ `spec.memoryGB`, `factor: 1024` — lossy on the integer side |
| 13 | [`ListJoin`](../strategies/list-join-split.md) | `spec.dnsServers` array ⇄ `spec.dnsServersCSV` string |

### `v1` spoke — 16 rules

| # | Strategy | What it maps here |
|---|---|---|
| 0 | [`SingletonArrayToObject`](../strategies/singleton-array-object.md) | `spec.zones` (one-element array) → `spec.zone` |
| 1 | [`ObjectToSingletonArray`](../strategies/singleton-array-object.md) | `spec.primaryRegion` → `spec.regions` |
| 2 | [`MapToFields`](../strategies/fields-map.md) | `spec.tags` map → `spec.envTag` + `spec.teamTag` |
| 3 | [`ToLabel`](../strategies/metadata-stash.md) | `spec.tier` → label, `serialization: String` |
| 4 | [`FromLabel`](../strategies/metadata-stash.md) | hub label → `spec.operatorTier`, `stashOnReverse: true` |
| 5 | [`DefaultValue`](../strategies/default-value.md) | `spec.computeUnits`, spoke-only, defaults to `1` |
| 6 | [`Delete`](../strategies/delete.md) | drops `spec.debugMode`, a `v1`-only escape hatch |
| 7 | [`ForEach`](../strategies/for-each.md) | per-element renames inside `spec.volumes` |
| 8 | [`FieldsToScalar`](../strategies/scalar-fields.md) | `spec.contactName` + `spec.contactEmail` → `spec.contact` |
| 9 | [`ArrayToMapByKey`](../strategies/array-map-key.md) | `spec.endpoints` array ⇄ map keyed by `name` |
| 10 | [`MapToArrayByKey`](../strategies/array-map-key.md) | `spec.limitsByTier` map ⇄ `spec.tierLimits` array keyed by `tier` |
| 11 | [`ListSplit`](../strategies/list-join-split.md) | `spec.allowedCIDRsCSV` string ⇄ `spec.allowedCIDRs` array |
| 12 | [`Quantity`](../strategies/quantity.md) | `spec.cpuRequest` Quantity string ⇄ `spec.cpuMillis` millivalue — lossy on the string side |
| 13 | [`Duration`](../strategies/duration.md) | `spec.timeout` duration string ⇄ `spec.timeoutSeconds` — lossy on the string side |
| 14 | [`MapKeyRename`](../strategies/map-key-rename.md) | `spec.extraLabels`: rename `app` ⇄ `application`, other keys pass through |
| 15 | [`CEL`](../strategies/cel.md) | `spec.packed` ⇄ `spec.bitHigh` + `spec.bitLow` (always lossy) |

## What the schema is quietly demonstrating

Reading [`xrd.yaml`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/internal/cli/testdata/full/xrd.yaml)
alongside the config explains two things no single strategy page can:

- **Fields with an identical shape on both sides need no rule.** Each spoke
  mirrors the fields *the other* spoke has rules for, keeping them
  byte-identical to the hub — and they are covered automatically. That is why
  30 rules are enough for a schema this wide, and why fail-closed coverage
  isn't as noisy in practice as it sounds.
- **`status` is not special.** `v2` maps `status.phase` → `status.state` with an
  ordinary `FieldRename`, while `v1` leaves `status` untouched because its shape
  already matches. A conversion webhook receives the whole stored object, so
  `status` follows exactly the same coverage rules as `spec`.

## Other commands worth running against it

Schema-only analysis, no samples needed — this is where the three rules the
engine cannot verify (`JSONPatch`, `ScalarToFields`, `FieldsToScalar`) surface
as warnings:

```console
go run ./cmd/convctl analyze \
  --xrd internal/cli/testdata/full/xrd.yaml \
  --config internal/cli/testdata/full/config.yaml
```

`convctl suggest` against the rule-stripped config, to see how much of a
mapping this wide can be bootstrapped automatically (a handful of
`FieldRename`s and one `TypeCoerce` — everything else is a design decision the
tool deliberately won't guess at):

```console
go run ./cmd/convctl suggest \
  --xrd internal/cli/testdata/full/xrd.yaml \
  --config internal/cli/testdata/full/config-norules.yaml
```

And `convctl diff` between the two configs, which reads as "what did writing all
30 rules actually accomplish?" — every field that stopped being uncovered, per
spoke:

```console
go run ./cmd/convctl diff \
  --xrd internal/cli/testdata/full/xrd.yaml \
  --config internal/cli/testdata/full/config-norules.yaml \
  --config internal/cli/testdata/full/config.yaml -o table
```

See the [CLI Reference](../cli.md) for every command and flag.

!!! note "It's a test fixture"
    Unit tests and the e2e suite assert against these files, so treat them as
    read-only when experimenting — copy the directory elsewhere before editing,
    or point `--config` at a copy.
