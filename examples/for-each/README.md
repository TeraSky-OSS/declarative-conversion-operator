# For each (array element reshape)

`xclusters.example.org` has a `spec.nodePools` array at the same path on both
versions, but each *element* changed: the hub's `instanceType`/`nodeCount`
were `machineType`/`replicas` in `v1`.

| File | What it is |
|---|---|
| `xrd.yaml` | The XRD. Same array path, different item schema. |
| `xrdconversionconfig.yaml` | One `ForEach` rule wrapping two `FieldRename` rules. |
| `samples/` | A two-element hub object and a one-element `v1` object. |

## The interesting part

The nested rules' paths are **relative to a single array element** —
`instanceType`, not `spec.nodePools[].instanceType`. `ForEach` establishes the
scope; the rules inside it are ordinary rules that happen to run once per
element.

`ForEach` adds no lossiness of its own: a `ForEach` of two `FieldRename` rules
is lossless in both directions, exactly like the rules it wraps. Two
constraints are worth knowing before you reach for it:

- **Nesting is capped at depth 2** — a `ForEach` may wrap another `ForEach`
  (arrays-of-arrays); a third level is rejected.
- **Positional correspondence is strict.** If both the hub and spoke item
  paths are present on the input as arrays of different lengths, conversion
  fails loudly rather than truncating to the shorter one.

## Run it

```console
convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl test     --config xrdconversionconfig.yaml --xrd xrd.yaml --samples ./samples/
```

Both samples pass in both directions, including the two-element one — element
count doesn't change what the rule does.

## Reference

- [For Each strategy](https://terasky-oss.github.io/declarative-conversion-operator/strategies/for-each/)
- [Limitations](https://terasky-oss.github.io/declarative-conversion-operator/limitations/)
