# Examples

Five conversion stories, smallest first. Most directories hold a schema, a
conversion config, and `samples/` — everything `convctl` needs offline.
[`crossplane-xr-multiversion/`](crossplane-xr-multiversion/) is a **staged**
walkthrough (XRD + Composition that writes a ConfigMap) rather than a single
end-state snapshot. [`demo.sh`](crossplane-xr-multiversion/demo.sh) runs the
full lifecycle on a live cluster.

| Example | Story | Strategies |
|---|---|---|
| [`field-rename/`](field-rename/) | One field was renamed between two versions. | `FieldRename` |
| [`enum-remap/`](enum-remap/) | The same field's allowed values were abbreviated. | `EnumRemap` |
| [`for-each/`](for-each/) | Each element of an array changed shape. | `ForEach` + `FieldRename` |
| [`crossplane-xr-multiversion/`](crossplane-xr-multiversion/) | Staged XRD lifecycle: v1 + ConfigMap Composition, add v2, promote the hub, add v3, promote v3 as the standard, deprecate v1. | `FieldRename` |
| [`native-crd/`](native-crd/) | The same model against a plain Kubernetes CRD instead of a Crossplane XRD. | `FieldRename`, `Delete` |

Every example runs the same two commands (paths differ per directory — each
README spells them out):

```console
convctl validate --config <config>.yaml --xrd xrd.yaml
convctl test     --config <config>.yaml --xrd xrd.yaml --samples ./samples/
```

From a checkout, `go run ./cmd/convctl` works in place of an installed
`convctl` binary:

```console
go run ./cmd/convctl test --config examples/field-rename/xrdconversionconfig.yaml \
  --xrd examples/field-rename/xrd.yaml --samples examples/field-rename/samples/
```

For a single fixture that exercises *every* built-in strategy at once, see
[`internal/cli/testdata/full/`](../internal/cli/testdata/full/) and its
[walkthrough](https://terasky-oss.github.io/declarative-conversion-operator/examples/kitchen-sink/).
