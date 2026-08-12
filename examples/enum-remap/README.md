# Enum remap

`xcaches.example.org` kept `spec.size` at the same path across both versions,
but the hub spells its values out (`Small`/`Medium`/`Large`) where `v1` used
one-letter codes (`S`/`M`/`L`).

| File | What it is |
|---|---|
| `xrd.yaml` | The XRD. Same field path on both versions, different `enum` lists. |
| `xrdconversionconfig.yaml` | One `EnumRemap` rule with a three-entry mapping. |
| `samples/` | One object at each served version. |

## The interesting part

`EnumRemap` is only lossless because the mapping is **total and unambiguous**:
every hub value appears exactly once and every spoke value appears exactly
once. The two ways to break that are caught at different times, which makes
this a good example of why both commands exist:

- **Ambiguous mapping — caught statically.** Point `Medium` and `Small` at the
  same spoke value and `convctl validate` rejects the config outright: the
  spoke→hub direction could no longer pick one, so it is lossy and would need
  `acknowledgeLossy: true`.
- **Incomplete mapping — caught by testing.** Delete the `Large`/`L` entry and
  `convctl validate` still passes, because with the default
  `onUnmappedHubValue: Error` nothing about the mapping is *lossy* — an
  unmapped value is a hard conversion failure instead. `convctl test` is what
  surfaces it, as `remapEnum: unmapped value "Large" at "spec.size"` on the
  sample that actually uses it.

The alternative to that error, `onUnmappedHubValue: Drop`, deliberately
discards unmapped values and therefore makes that direction lossy.

## Run it

## Run it

```console
convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl test     --config xrdconversionconfig.yaml --xrd xrd.yaml --samples ./samples/
```

## Reference

- [Enum Remap strategy](https://terasky-oss.github.io/declarative-conversion-operator/strategies/enum-remap/)
