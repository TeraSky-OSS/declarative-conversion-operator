# Adding a conversion strategy

This is the end-to-end checklist for adding a new named strategy to
`declarative-conversion-operator`. Every existing strategy follows the same
layers; if you touch fewer (or more) than the list below, something is wrong.

Worked example throughout: **`EnumRemap`** — a bidirectional scalar enum
rewriter. Follow its diffs as a template for a new strategy.

## Layers (in order)

| # | Layer | Touch these |
|---|---|---|
| 1 | Engine params + strategy name | `pkg/engine/rules.go` |
| 2 | Runtime `Op` | `pkg/engine/ops.go` (+ `ops_test.go` if useful) |
| 3 | Compile-time resolver | `pkg/engine/compile.go` (+ `compile_test.go`) |
| 4 | API / CRD types | `api/v1alpha1/xrdconversionconfig_types.go` |
| 5 | API → engine conversion | `api/v1alpha1/xrdconversionconfig_convert.go` (+ `_convert_test.go`) |
| 6 | Admission validation | `internal/webhook/xrdconversionconfig_webhook.go` (and the CRD twin if structure differs) |
| 7 | Generate | `make generate manifests helm-sync` |
| 8 | CLI fixture | `internal/cli/testdata/full/` (and/or a focused `examples/` story) |
| 9 | Docs | `docs/strategies/<name>.md` + `docs/strategies/index.md` + `mkdocs.yml` |

`CRDConversionConfig` shares the same `ConversionRule` type and the same engine
path — you do **not** duplicate strategy logic for native CRDs.

## 1. Engine: name + params

In [`pkg/engine/rules.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/pkg/engine/rules.go):

1. Add a `Strategy…` constant next to the others (`StrategyEnumRemap`).
2. Define a `…Params` struct that implements `RuleParams` via a private
   `isRuleParams()` method (`EnumRemapParams`).
3. Document losslessness expectations on the struct — the compile layer will
   enforce them.

`EnumRemapParams` carries the field path, the hub↔spoke mapping entries, and
`OnUnmappedHubValue` / `OnUnmappedSpokeValue` policies.

## 2. Engine: runtime `Op`

In [`pkg/engine/ops.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/pkg/engine/ops.go),
implement an `Op` that mutates an object in one direction. `EnumRemap` uses
`remapEnumOp`: look up the scalar at `path`, apply a precompiled map, fail or
drop on unknown values per policy.

Keep `Op`s dumb: no schema lookups at request time. Anything schema-dependent
belongs in the compile resolver (next step), which closes over resolved paths
and maps into the `Op`.

## 3. Engine: compile resolver

In [`pkg/engine/compile.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/pkg/engine/compile.go):

1. Add a `case YourParams:` arm in the rule switch (see `case EnumRemapParams:`).
2. Implement `resolveYourStrategy(...)` that:
   - resolves hub/spoke field paths against the live schemas
   - marks claimed leaves so uncovered-field detection stays accurate
   - returns hub→spoke and spoke→hub `Op`s plus a `LosslessVerdict`
   - emits `Diagnostic`s for path/type/enum problems
3. Add compile tests in `compile_test.go` (`TestEnumRemap_Bidirectional`,
   `TestEnumRemap_NonInjectiveIsLossy`).

Compile is where "can we prove this lossless?" is decided. Runtime only
executes the plan.

## 4–5. API types and conversion

In [`api/v1alpha1/xrdconversionconfig_types.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/api/v1alpha1/xrdconversionconfig_types.go):

1. Extend the `Strategy` kubebuilder `Enum=` list with the new name.
2. Add the matching `Strategy…` constant.
3. Add a `…Params` API struct (JSON tags, validation markers).
4. Add a pointer field on `ConversionRule` (`EnumRemap *EnumRemapParams`).

In [`api/v1alpha1/xrdconversionconfig_convert.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/api/v1alpha1/xrdconversionconfig_convert.go),
map the API rule into `engine.…Params` (`case StrategyEnumRemap:`). Cover it in
`xrdconversionconfig_convert_test.go`.

Then regenerate:

```console
make generate manifests helm-sync
```

## 6. Admission webhook

In [`internal/webhook/xrdconversionconfig_webhook.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/internal/webhook/xrdconversionconfig_webhook.go),
extend **both** stages the webhook runs at apply time:

1. **Structural validation** (`ValidateStructure`) — a rule with
   `strategy: EnumRemap` must carry `enumRemap:` (and must not carry unrelated
   param blocks).
2. **Live schema analysis** — when the target XRD exists, the webhook calls
   `engine.Analyze` against it and rejects invalid configs at admission. The
   controller and `convctl analyze`/`test` reuse that same analysis path; they
   are not the only places schema-aware checks run.

## 7. Fixture + offline proof

Add or extend a sample under `internal/cli/testdata/full/` (EnumRemap already
lives there) so `convctl test` exercises the new rule on a real object:

```console
go run ./cmd/convctl test \
  --xrd internal/cli/testdata/full/xrd.yaml \
  --config internal/cli/testdata/full/config.yaml \
  --samples internal/cli/testdata/full/samples
```

For a strategy newcomers will copy, also add a focused story under `examples/`
(see `examples/enum-remap/`).

## 8. Docs

1. New page `docs/strategies/<kebab-name>.md` — mirror
   [`docs/strategies/enum-remap.md`](../strategies/enum-remap.md): what it does,
   when to use it, lossiness callout, YAML example, field reference.
2. Link it from `docs/strategies/index.md` and `mkdocs.yml`.

## Definition of done

A contributor following this guide for a toy strategy should end up touching
**exactly** the layers above — no more, no fewer — and leave:

- `make test` green
- `convctl test` green against an updated fixture
- a docs page a user can find from the strategy index

## Related

- [Contributing](../../CONTRIBUTING.md) — day-to-day `make` loop
- [Strategy reference index](../strategies/index.md)
- [Kitchen sink fixture](../examples/kitchen-sink.md) — every strategy in one place
