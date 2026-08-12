# Examples

Five self-contained conversion stories, smallest first. Each directory holds a
schema (an XRD or a CRD), the conversion config for it, and a `samples/`
directory of objects at each served version — everything `convctl` needs to
validate and test the mapping offline, with no cluster involved.

| Example | Story | Strategies |
|---|---|---|
| [`field-rename/`](field-rename/) | One field was renamed between two versions. | `FieldRename` |
| [`enum-remap/`](enum-remap/) | The same field's allowed values were abbreviated. | `EnumRemap` |
| [`for-each/`](for-each/) | Each element of an array changed shape. | `ForEach` + `FieldRename` |
| [`crossplane-xr-multiversion/`](crossplane-xr-multiversion/) | A three-version XRD with a hub and two spokes, including one acknowledged lossy field. | `FieldRename`, `ScalarToObject`, `EnumRemap`, `SingletonArrayToObject`, `ToAnnotation`, `Delete` |
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
