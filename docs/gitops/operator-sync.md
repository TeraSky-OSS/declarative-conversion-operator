# GitOps: installing the operator (Flux / Argo)

Copy-pasteable trees live in
[`examples/gitops/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/gitops):

| Tree | Tool | What it shows |
|---|---|---|
| [`flux/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/gitops/flux) | Flux `HelmRelease` + two `Kustomization`s | CRDs + chart, then configs (`dependsOn` + `wait`) |
| [`argo/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/gitops/argo) | Two Argo `Application`s | Same order via sync-waves `0` then `1` |

Apply order is always:

```text
CRDs  →  ConversionWebhookServer (chart)  →  XRDConversionConfig / CRDConversionConfig
```

The controller already health-gates the last step (it will not patch a
target until the assigned `ConversionWebhookServer` is `Available`). The
GitOps annotations exist so Flux/Argo do not report the environment
failed while that gate is still open.

## `driftPolicy: KeepServingStale`

A GitOps reconcile is not an atomic multi-object transaction. A hub
promotion changes the schema and the conversion config independently.
During that window they disagree.

`KeepServingStale` (default, and set explicitly on the sample config)
keeps serving the last known-good plan. `FailClosed` unpatches
conversion the moment they disagree — the failure mode that turns a
routine Flux/Argo retry into an API outage. Use `FailClosed` only when a
human applies both objects in one shot and is watching.

Details: [examples/gitops/README.md](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/examples/gitops/README.md).

## Related

- [Fleet CI](fleet-ci.md) — `convctl test --live` / `diff --live` per cluster
- [Installation](../installation.md)
- XRD lifecycle demo (app GitOps, not operator install):
  [`examples/crossplane-xr-multiversion/gitops/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/gitops)
