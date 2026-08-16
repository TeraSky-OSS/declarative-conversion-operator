# GitOps: operator apply order (Flux and Argo)

These trees install the operator from the published Helm chart, then apply
an `XRDConversionConfig`. They are **not** the Crossplane XR lifecycle
demo — that lives in
[`examples/crossplane-xr-multiversion/gitops/`](../crossplane-xr-multiversion/gitops/).

## Apply order

1. **CRDs** — the chart's `crds/` directory (`XRDConversionConfig`,
   `CRDConversionConfig`, `ConversionWebhookServer`). Helm installs them
   on first `helm install`; Flux/Argo must be told to create-or-replace
   CRDs on upgrade too, or a later chart bump leaves the cluster on stale
   CRDs.
2. **Chart** — manager + the bootstrap `ConversionWebhookServer/default`.
   Wait until that CWS is `Available` before treating the install as
   ready. The controller will not patch a target XRD/CRD until this gate
   passes.
3. **Configs** — `XRDConversionConfig` / `CRDConversionConfig` (and the
   XRD/CRD they target). Applying a config before the CWS is ready is
   safe: the object stays `Pending` until every health gate passes. GitOps
   `wait` / sync-waves still matter so the tool does not report the
   environment failed during that window.

```text
CRDs  →  ConversionWebhookServer (chart)  →  XRDConversionConfig
```

[`flux/`](flux/) uses two Flux `Kustomization`s (`dependsOn` + `wait`).
[`argo/`](argo/) uses two Argo `Application`s and sync-waves.

## `driftPolicy` in continuous delivery

Use **`KeepServingStale`** (the default) on every config a GitOps tool
reconciles.

GitOps applies the XRD/CRD and the conversion config as **separate
objects**. A hub promotion is two fields that must move together
(`hubVersion` on the config, `referenceable`/`storage` on the schema).
They will disagree for at least one reconcile, often longer if Flux/Argo
retry independently.

- **`KeepServingStale`** marks the config `Stale` and **keeps serving the
  last known-good plan**. Conversions stay up while the two objects
  converge. That is the failure mode this default exists to avoid.
- **`FailClosed`** **stops serving conversions** the moment the schema
  and config disagree. The apiserver then fails `ConversionReview`s.
  Flux/Argo see XRs/CRs as unhealthy, retry, and can widen the outage.
  Reserve `FailClosed` for a human-driven apply where both objects change
  in one `kubectl` invocation you are watching.

See [Changing the hub version](https://terasky-oss.github.io/declarative-conversion-operator/configuration/xrdconversionconfig/#changing-the-hub-version).

## Prerequisites

cert-manager (and Crossplane, if you apply the sample XRD) must already
be on the cluster. The chart does not vendor either.

## Sample config

[`configs/`](configs/) is the [`field-rename`](../field-rename/) XRD plus
its conversion config, with `driftPolicy: KeepServingStale` written
explicitly so a reviewer sees the CD choice.
