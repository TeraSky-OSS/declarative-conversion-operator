# XRDConversionConfig

One `XRDConversionConfig` targets exactly one Crossplane `CompositeResourceDefinition` (enforced by a unique index plus the admission webhook — two configs can never race to patch the same XRD). It's cluster-scoped, matching the XRD it targets.

## Spec

```yaml
apiVersion: terasky.com/v1alpha1
kind: XRDConversionConfig
metadata:
  name: xwidgets-conversion
spec:
  targetXRD:
    name: xwidgets.example.org        # the XRD's metadata.name
  hubVersion: v3                       # must equal the XRD's referenceable version
  spokes:
    - version: v2
      rules: [ ... ]                   # see the Strategy Reference
    - version: v1
      rules: [ ... ]

  # optional
  webhookServerRef:
    name: tenant-a-webhook             # defaults to whichever instance has spec.default: true
  conversionReviewVersions: ["v1"]     # default
  unmappedFieldPolicy: Error           # Error (default) | Warn
  unmappedFieldReason: ""              # required if unmappedFieldPolicy: Warn
  driftPolicy: KeepServingStale        # KeepServingStale (default) | FailClosed
```

| Field | Description |
|---|---|
| `targetXRD.name` | The XRD's `metadata.name` (for XRDs, always `<plural>.<group>`). |
| `hubVersion` | Must equal the XRD's `referenceable: true` version — validation fails otherwise. This is Crossplane's storage version; every spoke is converted to/from it. |
| `spokes[].version` / `spokes[].rules` | One entry per served spoke version, each with its own rule list. A version not listed here — including the hub itself — needs no rules only if every one of its fields is structurally identical to the hub; otherwise it must appear with an explicit `Delete`/`DefaultValue`/etc. rule for the differing fields. |
| `webhookServerRef` | Pins this config to a specific `ConversionWebhookServer` by name. Omit to use whichever instance has `spec.default: true`. |
| `conversionReviewVersions` | The `ConversionReview` API versions the webhook accepts, passed through to the XRD's `spec.conversion.webhook.conversionReviewVersions`. |
| `unmappedFieldPolicy` | `Error` (default): any hub or spoke field left unclaimed by a rule and not structurally identical on both sides fails validation — "unknown means assume lossy, never silently pass." `Warn` downgrades this to a warning for every uncovered field; requires `unmappedFieldReason`. |
| `unmappedFieldReason` | Documents why this config is expected to leave fields unmapped. Required whenever `unmappedFieldPolicy: Warn` is set (the admission webhook rejects `Warn` with an empty reason). Setting it alone — even under the default `Error` policy — also downgrades uncovered-field failures to warnings for this config, so it's a second, independent way to accept the same risk with a recorded justification, without switching the whole config to `Warn`. |
| `driftPolicy` | Governs what happens when the live XRD's schema no longer matches what this config last validated against — see [Drift handling](#drift-handling). |

## Rules

Every rule picks a `strategy` and sets exactly one matching params field:

```yaml
rules:
  - strategy: FieldRename
    fieldRename:
      hubPath: spec.storageGB
      spokePath: spec.storageSize
    acknowledgeLossy: false   # required true if the engine determines this rule is lossy
    reason: ""                 # encouraged whenever acknowledgeLossy is true
```

See the [Strategy Reference](../strategies/index.md) for every strategy's params, semantics, and worked examples. Every rule the engine determines is lossy in *either* direction requires `acknowledgeLossy: true`, or the whole config is rejected as `Invalid` — this is checked identically by the admission webhook (at `kubectl apply` time) and the controller (on every reconcile, in case the XRD's schema has drifted since).

### `status` fields work exactly like `spec` fields

A CRD conversion webhook receives the entire stored object, `status` included, regardless of whether the CRD uses the `status` subresource — and `pkg/engine` compiles and converts whatever top-level schema the XRD version declares, never narrowing to `.spec`. So a `fieldRename` (or any other strategy) targeting `status.somePath` behaves identically to one targeting `spec.somePath`, and the same fail-closed coverage rule applies: a `status` field that differs in shape between hub and spoke needs an explicit rule, or the config is rejected.

```yaml
- strategy: FieldRename
  fieldRename:
    hubPath: status.phase
    spokePath: status.state
```

## Status

```yaml
status:
  observedGeneration: 3
  observedXRDGeneration: 7
  schemaHash: "a1b2c3..."
  phase: Applied
  overallLossless: true
  assignedWebhookServer: default
  webhookPath: /convert/xwidgets.example.org
  webhookURL: https://default-webhook-server.declarative-conversion-system.svc/convert/xwidgets.example.org
  conditions:
    - type: Validated
      status: "True"
      reason: Validated
    - type: XRDHealthy
      status: "True"
      reason: Established
    - type: WebhookServerReady
      status: "True"
      reason: ServerReady
    - type: Applied
      status: "True"
      reason: Applied
  spokeStatuses:
    - version: v2
      lossless: {hubToSpoke: true, spokeToHub: true}
      fieldsUncoveredHub: []
      fieldsUncoveredSpoke: []
      ruleResults: [...]
```

### Phases

| Phase | Meaning |
|---|---|
| `Pending` | Not yet reconciled. |
| `Validating` | Reconcile in progress. |
| `Validated` | Rules compiled cleanly, but the XRD or webhook server isn't healthy yet — see `conditions` for which. |
| `Invalid` | Compilation failed: a schema mismatch, an unacknowledged lossy rule, or an uncovered field. **The XRD is never touched in this phase.** |
| `Applied` | `spec.conversion` has been patched onto the XRD. Conversions are live. |
| `Stale` | Was `Applied`, but the live XRD's schema has drifted since and re-validation now fails. See [Drift handling](#drift-handling) — the last-known-good plan keeps serving by default. |
| `Reverting` | Deletion in progress; unpatching the XRD. |
| `Failed` | An unexpected error, distinct from a validation failure. |

### Conditions

| Condition | Meaning when `True` |
|---|---|
| `Validated` | The rule set compiles cleanly against the XRD's schemas: every field covered, no unacknowledged lossy rule. |
| `XRDHealthy` | The target XRD exists and its `Established` condition is `True`. |
| `WebhookServerReady` | The assigned `ConversionWebhookServer` instance's Deployment is `Available`, its `Service` has ready endpoints, and its certificate is ready. |
| `Applied` | `spec.conversion` has been patched onto the XRD. |
| `Stale` | The live XRD's schema no longer matches what was last validated (see below). |
| `DeletionBlocked` | Deletion is being held by the finalizer — see [Deletion safety](#deletion-safety). |

`status.spokeStatuses` always reflects the *result of the last validation attempt*, independent of whether that validation ultimately let the config reach `Applied` — so you can inspect exactly which fields are uncovered, or which rule is lossy, even while the config is `Invalid`.

## Ordering: nothing touches the XRD until every gate passes

On every reconcile, in order:

1. Fetch the target XRD. Not found → `Invalid`, stop.
2. Extract hub/spoke schemas; confirm `hubVersion` matches the XRD's referenceable version and every spoke is `served`.
3. Compile the rules (`pkg/engine.Compile`). Any error, unacknowledged lossy rule, or uncovered field → `Invalid`, **the XRD is never touched**.
4. Resolve the target `ConversionWebhookServer` (explicit `webhookServerRef`, or whichever instance is `default`).
5. Confirm the XRD is `Established`.
6. Confirm the assigned `ConversionWebhookServer`'s Deployment is `Available`, its Service has ready endpoints, and its certificate is ready.
7. **Only now**: server-side-apply `spec.conversion` onto the XRD, scoped to just that field (plus a couple of tracking annotations) — never a full-object apply, so this never fights any other owner of the XRD.

## Drift handling

Every reconcile recomputes a hash of the live XRD's schema plus this config's rules. If it still matches what was last validated, nothing changes — a clean re-validation is silent. If it no longer matches:

- **`KeepServingStale`** (default): the config is marked `Stale` loudly (condition, event, metric), but **keeps serving the last known-good compiled plan**. Unpatching or dropping a working conversion webhook on drift-detection would itself be the outage this operator exists to prevent.
- **`FailClosed`**: stops serving conversions for the XRD as soon as drift is detected, for organizations that would rather fail loud immediately than risk serving a plan validated against a schema that no longer exists.

## Changing the hub version

`hubVersion` must equal the XRD's live referenceable (storage) version — this is a **reconcile-time check**, not a one-time gate, so it stays enforced for the config's entire life. That's what makes promoting a new version to be the hub safe, via `DriftPolicy: KeepServingStale` (the default):

1. Add the new version (say `v3`) to the XRD as an ordinary **served, non-`referenceable`** spoke, with rules in this config relative to the *current* hub. Clients can start using `v3` immediately, round-tripping through the existing hub like any other spoke.
2. When ready to promote `v3` to hub, do both of the following — the order between them doesn't matter:
   - Update this `XRDConversionConfig` to declare `hubVersion: v3`, with the old hub (say `v2`) rewritten as a spoke *relative to `v3`*. This isn't a mechanical copy: rule direction and paths are expressed from the hub's perspective, so the old hub's rules need to be re-authored as a spoke's rules, not just swapped.
   - Flip `referenceable: true` from `v2` to `v3` on the XRD itself (and `referenceable: false` on `v2`).
3. Whichever of those two changes lands first, `hubVersion` and the live referenceable version will briefly disagree. The next reconcile re-validates as `Invalid`/`Stale` — but per `KeepServingStale`, the controller never un-patches the XRD; it keeps serving the last-known-good plan (still hub=`v2`) the whole time. Since `v3` was already a working spoke from step 1, conversions keep functioning correctly through the old plan throughout — Kubernetes' conversion webhook protocol converts between whatever version is stored and whatever version was requested per-request, so a router internally pivoting through `v2` handles a `v3`-desired request exactly as it always did.
4. Once both changes have landed, the next reconcile finds `hubVersion` matching the live referenceable version again, validates cleanly, and re-patches the XRD with the new `v3`-anchored plan — no outage, no manual intervention to "resume."

`DriftPolicy: FailClosed` does **not** offer this safety net — a mismatch under `FailClosed` stops serving conversions immediately, so plan the two updates as a single fast operation (or temporarily switch to `KeepServingStale` for the migration) if you use that policy.

One K8s-level detail this doesn't handle: objects already in etcd remain physically encoded at whichever version was `referenceable` when they were last written. The apiserver's conversion machinery serves them correctly regardless, but if you want them actually *rewritten* at the new storage version, that's standard Kubernetes housekeeping unrelated to this operator — e.g. `kubectl get <resource> --all-namespaces -o json | kubectl replace -f -`, or a [`StorageVersionMigration`](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/#upgrade-existing-objects-to-a-new-stored-version) if your cluster runs the storage-version-migrator.

## Deletion safety

Deleting an `XRDConversionConfig` runs a finalizer-gated "safe revert," not an immediate removal:

- If it was never `Applied`, or the target XRD is already gone, the finalizer is removed immediately — nothing to revert.
- If the XRD still has **more than one served version**, reverting to `strategy: None` would risk serving objects in the wrong shape to any client still using a non-storage version — **deletion is blocked** (`DeletionBlocked` condition) unless the object carries the break-glass annotation, checked live at the moment of the delete reconcile:

  ```console
  kubectl annotate xrdconversionconfig xwidgets-conversion \
    conversion.terasky.com/allow-unsafe-delete=true
  kubectl delete xrdconversionconfig xwidgets-conversion
  ```

- Otherwise (or with the break-glass annotation present), the operator server-side-applies `spec.conversion: {strategy: None}` onto the XRD, strips its own tracking annotations, and removes the finalizer.
