# Examples

The [`examples/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples)
directory in the repository holds five self-contained conversion stories,
smallest first. Each one is a directory with a schema (an XRD or a CRD), the
conversion config for it, sample objects at every served version, and a README
explaining the scenario — everything `convctl` needs to validate and test the
mapping offline, with no cluster involved.

Where the [Strategy Reference](../strategies/index.md) shows one strategy in
isolation, these show a complete, runnable config you can copy and adapt.

## The gallery

| Example | Story | Strategies |
|---|---|---|
| [`field-rename/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/field-rename) | One field was renamed between two versions — the smallest useful config, and a demonstration of why identical fields need no rule. | [`FieldRename`](../strategies/field-rename.md) |
| [`enum-remap/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/enum-remap) | The same field's allowed values were abbreviated (`Large` → `L`). Shows which mistakes `validate` catches versus which only `test` catches. | [`EnumRemap`](../strategies/enum-remap.md) |
| [`for-each/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/for-each) | Every element of an array changed shape, with nested rules scoped to one element. | [`ForEach`](../strategies/for-each.md) |
| [`crossplane-xr-multiversion/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion) | Staged Crossplane XR lifecycle: one-version XRD + ConfigMap Composition, add a spoke, promote the hub (new Composition + retarget `compositionRef`), add `v3`, promote `v3` as the standard, deprecate `v1` (including `convctl migrate-storage` and dropping the version block). GitOps alternative: [`gitops/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/gitops) + [`convctl generate kyverno`](../cli.md#convctl-generate-kyverno) (`--gitops-engine simulate\|flux\|argo`). | [`FieldRename`](../strategies/field-rename.md) — see the [lifecycle walkthrough](xr-lifecycle.md) |
| [`native-crd/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/native-crd) | The same model against a plain Kubernetes CRD, with no Crossplane anywhere. | [`FieldRename`](../strategies/field-rename.md), [`Delete`](../strategies/delete.md) |

Operator install via Flux or Argo (apply order and `driftPolicy`) is
[`examples/gitops/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/gitops)
— see [GitOps operator sync](../gitops/operator-sync.md).

For a single fixture that exercises *every* built-in strategy at once, see the
[kitchen-sink walkthrough](kitchen-sink.md).

## Running one

XRD-backed examples take `--xrd`; the native-CRD example takes `--crd` instead
(same flags `convctl` uses everywhere else):

```console
git clone https://github.com/terasky-oss/declarative-conversion-operator
cd declarative-conversion-operator/examples/field-rename

convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl test     --config xrdconversionconfig.yaml --xrd xrd.yaml --samples ./samples/
```

For `examples/native-crd/`, swap in `--crd crd.yaml` and
`crdconversionconfig.yaml`.

From a checkout, `go run ./cmd/convctl` works in place of an installed
`convctl` binary:

```console
go run ./cmd/convctl test --config examples/field-rename/xrdconversionconfig.yaml \
  --xrd examples/field-rename/xrd.yaml --samples examples/field-rename/samples/
```

`convctl validate` runs the same static checks the admission webhook would;
`convctl test` round-trips every sample through every served-version path and
grades the result. Both exit non-zero on a problem, so an example is also a
working template for a CI gate — see the [CLI Reference](../cli.md) for the
full command set and the exit-code matrix.

## Turning an example into a real config

The examples are deliberately generic (`example.org`, `XWidget`). To adapt one:

1. Replace `xrd.yaml`/`crd.yaml` with your real schema, or drop it and point
   `--xrd` at the file you already have.
2. Rewrite `spec.targetXRD.name`/`spec.targetCRD.name` and `spec.hubVersion` to
   match. The hub must be the storage version — `referenceable: true` on an
   XRD, `storage: true` on a CRD.
3. Run `convctl suggest` to draft rules for the fields nothing covers yet, then
   `validate` and `test` what you keep.
4. Before applying to a cluster that already has objects, run
   `convctl test --live` — it sources samples from the cluster instead of
   `samples/`, so you find out whether the mapping holds up against real data
   rather than only your fixtures.
