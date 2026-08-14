# CRDConversionConfig

`CRDConversionConfig` is `XRDConversionConfig`'s sibling for plain native Kubernetes `CustomResourceDefinition`s: identical spec shape, identical rule vocabulary (`ConversionRule`/`Strategy` — the same types, not a duplicated copy), identical controller ordering and safe-delete semantics. The only difference is which kind of resource it targets and patches. If you've already read [XRDConversionConfig](xrdconversionconfig.md), everything there applies here with `targetXRD` renamed to `targetCRD`.

## Spec

```yaml
apiVersion: terasky.com/v1alpha1
kind: CRDConversionConfig
metadata:
  name: widgets-conversion
spec:
  targetCRD:
    name: widgets.example.org        # the CRD's metadata.name
  hubVersion: v2                      # must equal the CRD's storage version
  spokes:
    - version: v1
      rules: [ ... ]                  # see the Strategy Reference — identical rule syntax

  # optional, identical semantics to XRDConversionConfig
  webhookServerRef:
    name: tenant-a-webhook
  conversionReviewVersions: ["v1"]
  unmappedFieldPolicy: Error
  unmappedFieldReason: ""
  driftPolicy: KeepServingStale
```

| Field | Description |
|---|---|
| `targetCRD.name` | The CRD's `metadata.name` (for CRDs, always `<plural>.<group>`). |
| `hubVersion` | Must equal the CRD's `storage: true` version — the native-CRD equivalent of an XRD's `referenceable: true`. |
| `spokes[].version` / `spokes[].rules` | Identical to `XRDConversionConfig`. |
| `webhookServerRef`, `conversionReviewVersions`, `unmappedFieldPolicy`, `unmappedFieldReason`, `driftPolicy` | Identical semantics to `XRDConversionConfig` — see there for details. |

## Status

Structurally identical to `XRDConversionConfig`'s status, with two native-CRD-specific differences:

- `status.observedCRDGeneration` instead of `status.observedXRDGeneration`.
- The health condition is named `CRDHealthy` instead of `XRDHealthy` — `True` once the target `CustomResourceDefinition` exists and its own `Established` condition is `True`.

Every other condition (`Validated`, `WebhookServerReady`, `Applied`, `Stale`, `DeletionBlocked`), phase, and `spokeStatuses` field behaves identically — see [XRDConversionConfig: Status](xrdconversionconfig.md#status) for the full reference.

## Ordering and safety

The gate sequence, drift handling, and deletion safety are byte-for-byte the same algorithm as `XRDConversionConfig`'s (see [there](xrdconversionconfig.md#ordering-nothing-touches-the-xrd-until-every-gate-passes) for the full walkthrough) — validate, resolve the assigned `ConversionWebhookServer`, confirm the CRD is `Established`, confirm the server is ready, and only then server-side-apply `spec.conversion` onto the CRD. The same `conversion.terasky.com/allow-unsafe-delete` break-glass annotation applies for deleting a config while the CRD still serves more than one version.

Promoting a different version to be the hub works the same way too — including why it's safe under the default drift policy — see [XRDConversionConfig: Changing the hub version](xrdconversionconfig.md#changing-the-hub-version), reading `storage: true`/`storage: false` wherever it says `referenceable`.

Unlike an XRD hub promotion, flipping `storage: true` does **not** write existing CRs. There is no Composition/`compositionRef` retarget that persists every object at the new storage version. Run [`convctl migrate-storage --crd NAME --prune-stored-versions`](../cli.md#convctl-migrate-storage) before dropping an old version from the CRD — that rewrite is the critical path, not optional housekeeping.

## Enabling and disabling native CRD support

Native CRD support is on by default (`--enable-crd-support=true` on the manager, `features.nativeCRD.enabled: true` in the Helm chart). Unlike XRD support, it carries no startup-crash risk if disabled — `CustomResourceDefinition` is a core Kubernetes type that always exists — but you can still turn it off if you simply don't want the feature active. See [Installation: Feature toggles](../installation.md#feature-toggles).

If native CRD support is disabled, creating a `CRDConversionConfig` is rejected outright by the admission webhook with a clear error, rather than silently accepted and left unreconciled forever.
