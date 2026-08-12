# Native CRD conversion

The same conversion model against a plain Kubernetes
`CustomResourceDefinition` instead of a Crossplane XRD. No Crossplane involved
anywhere — this works on a cluster that has never heard of it.

| File | What it is |
|---|---|
| `crd.yaml` | An `apiextensions.k8s.io/v1` CRD. `v2` is `storage: true`; `v1` is served but not stored. |
| `crdconversionconfig.yaml` | A `CRDConversionConfig` — same rule vocabulary as an `XRDConversionConfig`. |
| `samples/` | One object at each served version. |

## The interesting part

Only two things differ from the XRD examples:

- The config `kind` is `CRDConversionConfig` and it points at
  `spec.targetCRD.name` instead of `spec.targetXRD.name`.
- The hub must be the CRD's storage version (`storage: true`), which is the
  native-CRD equivalent of an XRD's `referenceable: true`. Naming a
  non-storage version as the hub is a hard validation error.

Everything else — the `rules` list, the strategy names, the fail-closed
coverage requirement, `acknowledgeLossy` — is identical, because both config
kinds compile down to the same engine.

`spec.legacyDebugFlag` exists only on the hub and is dropped on the way to
`v1`. That is lossy, so the rule carries `acknowledgeLossy: true` and a
`reason` explaining the decision; without them the config is rejected.

## Run it

Note `--crd` in place of `--xrd`. Which kind of config you have is determined
by the file's own `kind`, so passing the wrong flag is a clear error rather
than a silent mismatch:

```console
convctl validate --config crdconversionconfig.yaml --crd crd.yaml
convctl test     --config crdconversionconfig.yaml --crd crd.yaml --samples ./samples/
```

The `v2→v1` path reports `LOSS(acknowledged)` on `spec.legacyDebugFlag` and the
run exits `0`.

To use this on a cluster, install the operator with
`features.nativeCRD.enabled: true` (the default). `features.crossplane.enabled`
can be `false` — see
[Feature toggles](https://terasky-oss.github.io/declarative-conversion-operator/installation/#feature-toggles).

## Reference

- [CRDConversionConfig reference](https://terasky-oss.github.io/declarative-conversion-operator/configuration/crdconversionconfig/)
- [Field Rename](https://terasky-oss.github.io/declarative-conversion-operator/strategies/field-rename/) ·
  [Delete](https://terasky-oss.github.io/declarative-conversion-operator/strategies/delete/)
