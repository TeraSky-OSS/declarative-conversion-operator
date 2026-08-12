# Field rename

The smallest useful conversion: `xbuckets.example.org` has two served
versions, and the only difference between them is that the hub's
`spec.storageGB` was called `spec.storageSize` in `v1`.

| File | What it is |
|---|---|
| `xrd.yaml` | The XRD. `v2` is the hub (`referenceable: true`), `v1` is the spoke. |
| `xrdconversionconfig.yaml` | One `FieldRename` rule. |
| `samples/` | One object at each served version. |

## The interesting part

The XRD's `spec.region` and `status.phase` have identical shapes on both
versions, so **no rule covers them** — the engine passes structurally
identical fields through automatically. Rules are only needed where the two
schemas actually disagree.

That is also why fail-closed coverage isn't noisy in practice: you write rules
for the fields that changed, not for the whole schema. Delete the `FieldRename`
rule from the config and `convctl validate` rejects it, naming
`spec.storageGB` and `spec.storageSize` as uncovered.

`FieldRename` is lossless in both directions, so nothing here needs
`acknowledgeLossy`.

## Run it

```console
convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl test     --config xrdconversionconfig.yaml --xrd xrd.yaml --samples ./samples/
```

`convctl test` converts both samples along every served-version path
(`v1→v2`, `v2→v1`, plus each identity path) and reports 4 passing paths and no
loss. To see one conversion's output instead of a graded report:

```console
convctl convert --config xrdconversionconfig.yaml --xrd xrd.yaml \
  --sample samples/bucket-v1.yaml --to v2
```

## Reference

- [Field Rename strategy](https://terasky-oss.github.io/declarative-conversion-operator/strategies/field-rename/)
- [XRDConversionConfig reference](https://terasky-oss.github.io/declarative-conversion-operator/configuration/xrdconversionconfig/)
