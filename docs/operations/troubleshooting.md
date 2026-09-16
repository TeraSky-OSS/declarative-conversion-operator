# Troubleshooting

Symptoms you can observe from outside, what actually causes them, and what to
do. Every phase, condition, and metric named here is one the operator really
emits — see [XRDConversionConfig status](../configuration/xrdconversionconfig.md#status),
[ConversionWebhookServer status](../configuration/conversionwebhookserver.md#status),
and the [metric catalog](../observability.md).

## Triage in four commands

```console
# 1. What phase is the config in, and what's blocking it?
kubectl get xrdconversionconfig,crdconversionconfig
kubectl describe xrdconversionconfig <name>

# 2. Is the webhook server that serves it healthy?
kubectl get conversionwebhookserver

# 3. What does the operator think happened?
kubectl -n declarative-conversion-system logs deploy/declarative-conversion-operator-manager

# 4. Reproduce the validation offline, against the live schema
convctl diff --config <your-config>.yaml --live
```

`status.spokeStatuses` on the config always reflects the **last validation
attempt**, even when that attempt failed — so it tells you which field is
uncovered or which rule is lossy while the config is still `Invalid`.

## The config never reaches `Applied`

| Phase | `conditions` | Cause | Fix |
|---|---|---|---|
| `Invalid` | `Validated=False` | A rule is lossy in some direction and lacks `acknowledgeLossy: true`, or a hub/spoke field is covered by no rule and isn't structurally identical on both sides. | Read the condition message and `status.spokeStatuses[].fieldsUncovered*`. Add the missing rule, or `acknowledgeLossy: true` plus a `reason`. |
| `Invalid` | `Validated=False`, message names the hub version | `spec.hubVersion` isn't the target's storage version (`referenceable: true` on an XRD, `storage: true` on a CRD). | Point `hubVersion` at the real storage version — it can't be an arbitrary spoke. |
| `Invalid` | `XRDHealthy=False` / `CRDHealthy=False` | The target XRD/CRD doesn't exist, or its `Established` condition isn't `True`. | Check the name in `spec.targetXRD.name`/`spec.targetCRD.name`; `kubectl get crd <name>` and wait for `Established`. |
| `Validated` | `WebhookServerReady=False` | The assigned `ConversionWebhookServer`'s Deployment isn't `Available`, its Service has no ready endpoints, or its certificate isn't ready. | Debug the `ConversionWebhookServer` first — see [below](#the-conversionwebhookserver-never-becomes-available). Nothing is patched onto the target until it is ready. |
| `Validated` / `Stale` | `HandoverReady=False`, reason `HandoverPending` | The config has been **moved** to another `ConversionWebhookServer` (a changed `webhookServerRef`, or a sharding rebalance) and the destination has not yet reported that it can serve the target. This is the gate working: the target is still pointed at, and still served by, its current instance. | Usually resolves in seconds. If it does not, check `kubectl get conversionwebhookserver <destination> -o jsonpath='{.status.reportingReplicas}/{.status.servedTargets}'` — a `reportingReplicas` below `readyReplicas` means a ready replica has not published its Lease yet. |
| `Validated` / `Stale` | `HandoverReady=False`, reason `HandoverUnknown` | A replica of the destination could not state what it serves (its target set exceeded the annotation cap, or the annotation would not decode). | The instance is holding an implausible number of targets, or something else is writing its Leases. Check `kubectl -n <ns> get leases -l conversion.terasky.com/webhook-server=<destination>`. |
| `Failed` | — | An unexpected error, distinct from a validation failure. | Manager logs. This is the phase that should never be normal; it's worth an issue if the cause isn't obviously environmental. |

**The target resource is never patched in any of these phases.** A config that
won't validate cannot break conversions that are already working, because it
never gets that far.

To iterate faster than a reconcile loop, reproduce the same checks locally —
`convctl` runs the identical engine:

```console
convctl validate --config config.yaml --xrd xrd.yaml
convctl analyze  --config config.yaml --xrd xrd.yaml   # which direction is lossy, and why
```

## The `ConversionWebhookServer` never becomes `Available`

| `conditions` | Cause | Fix |
|---|---|---|
| `CertificateReady=False` | cert-manager isn't installed, or the `issuerRef` points at an `Issuer`/`ClusterIssuer` that doesn't exist or can't sign. | `kubectl get certificate,certificaterequest -n <cws-namespace>` and read cert-manager's own events. The chart creates a bootstrap self-signed `ClusterIssuer` only when you don't supply `certManager.issuerRef`. |
| `ServiceReady=False` | No pods are ready behind the Service. | `kubectl get pods -n <cws-namespace> -l app.kubernetes.io/name=...` and read the pod's logs/events — usually image pull, resources, or a rejected `spec.extraArgs`. |
| `Available=False` | The owned Deployment isn't available. | Same as above; the Deployment is created by the operator from the CR, not by Helm, so edit the CR (or chart values) rather than the Deployment. |
| `DefaultConflict=True` | More than one instance is marked `spec.default: true`. | The reconciler flags this and deliberately does **not** pick a winner. Unset `default` on all but one. |

## Conversions fail even though the config is `Applied`

`Applied` means the operator patched `spec.conversion` onto the target and the
webhook server was ready at that moment. Failures after that are on the data
path.

**Symptom: the apiserver returns `conversion webhook ... failed` on reads or
writes.**

- Check whether every *ready* replica has actually compiled a plan for that
  target. Desired assignment on the CWS (`status.assignedConfigs`) is not proof
  that a given pod has loaded it — that state is per-pod:

  ```promql
  (dco_webhook_ready == 1)
    unless on (pod)
  (dco_webhook_registry_entry_loaded{target="xwidgets.example.org"} == 1)
  ```

  An empty result is healthy. A pod in the result set is serving without a plan
  for that target and will answer `not_registered`.

- Look at `dco_webhook_registry_compile_errors_total{target=...}` and its
  `reason` label (`XRDNotFound`, `InvalidRules`, `AnalyzeFailed`,
  `ValidationErrors`, …). A compile failure leaves the previous plan in place
  rather than dropping it, so this can be true while conversions still work —
  which is exactly why it's alerted on (`ConversionWebhookRegistryCompileErrors`)
  rather than left to be noticed during an outage.

- For a break-glass look at one pod's actual state, `GET /debug/registry` on the
  plain-HTTP port returns a JSON snapshot. Prefer the metrics above for anything
  routine.

**Symptom: specific objects fail to convert while others succeed.** These are
runtime, data-dependent errors — the config is valid, but a particular value
isn't convertible. The message names the strategy and path:

| Message | Cause | Fix |
|---|---|---|
| `remapEnum: unmapped value "X" at "spec.size"` | An [`EnumRemap`](../strategies/enum-remap.md) mapping doesn't cover a value objects actually hold. | Add the mapping entry (or accept `onUnmapped...Value: Drop`, which makes that direction lossy). |
| `forEach` length mismatch | The hub and spoke arrays are both present with different lengths — [`ForEach`](../strategies/for-each.md) requires strict positional correspondence. | Fix the data, or model the field with a strategy that doesn't assume correspondence. |
| duplicate key from `arrayToMapByKey` | Two array elements share the same `keyField` value. | Duplicate keys can't round-trip; the engine fails rather than silently overwriting. |
| `typeCoerce` parse failure | A value genuinely isn't parseable as the other side's type (`"abc"` → integer). | Data problem, not a config problem. |

The tool for finding all of these *before* they hit production is
`convctl test --live`, which sources samples from the cluster instead of
fixtures:

```console
convctl test --xrd xrd.yaml --config config.yaml --live
```

## `Applied`, but the last move was never verified

`HandoverReady=True` with reason `HandoverUnverified` is not a failure: the
move completed and the target is patched. What it says is that the
destination published no served-target Leases *within the 30-second grace
period*, so the operator could not confirm the instance was ready before
repointing — it proceeded the way every release before this feature did.

Seeing `HandoverReady=False` with reason `HandoverAwaitingReports` instead
means that wait is still running. It resolves on its own within thirty
seconds, either into a verified move or into the unverified one below.

| Cause | Fix |
|---|---|
| The instance's replicas cannot write Leases in their namespace. | Almost always missing RBAC. An instance whose `spec.namespace` is not the release namespace needs the webhook-server Lease `Role` and `RoleBinding` created there — see [RBAC](../security/rbac.md). |
| A fleet mid-upgrade, where the replicas predate the feature. | Nothing to do; it resolves once every replica is on a version that publishes. |
| The replicas have no downward-API identity (`POD_NAME`/`POD_NAMESPACE`/`POD_UID`). | The operator sets these on every Deployment it reconciles. A Deployment that predates that is re-applied on the next reconcile of its `ConversionWebhookServer`. |

```console
kubectl -n <instance-namespace> get leases -l conversion.terasky.com/webhook-server=<instance>
kubectl get conversionwebhookserver <instance> -o jsonpath='{.status.reportingReplicas}'
```

Until it is fixed, a move onto that instance is unverified — which is the
pre-existing behaviour, not a new hazard, but it is the one window in which
a rebalance could briefly fail conversions. The grace period narrows that
window to fleets that genuinely cannot publish, rather than every fleet
whose replicas had not got round to it.

## `Applied` but not `Propagated`

**Symptom.** The config reports `Phase: Applied` and `Applied=True`, but reads at a non-storage version come back **relabelled and unconverted** — right `apiVersion`, old field layout, HTTP 200, no error.

```console
kubectl get xrdconversionconfig <name> \
  -o jsonpath='{.status.conditions[?(@.type=="ConversionPropagated")]}{"\n"}'
kubectl get xrdconversionconfig <name> -o jsonpath='{.status.generatedCRDs}{"\n"}' | jq
```

`Applied` means the operator patched `spec.conversion` onto the XRD. Nothing converts anything until **Crossplane** re-renders the generated CRD with that webhook block, and until it does the CRD sits on `strategy: None` — where the apiserver relabels stored objects rather than converting them. Normally that gap is seconds. It stops being seconds when Crossplane is not running, is unhealthy, has lost RBAC on `customresourcedefinitions`, or is reconciling a paused XRD.

| Reason | What it means | Where to look |
|---|---|---|
| `GeneratedCRDNotFound` | Crossplane has not created the CRD at all. | Is Crossplane running? Is the XRD `Established`? Does a `crossplane.io/paused` annotation sit on it? |
| `NotPropagated` | The CRD exists but does not carry the webhook, or carries a different one. The per-CRD `message` says which. | Crossplane's logs for the XRD definition controller; whether something else also writes this CRD. |
| `CABundleStale` | The CRD's `caBundle` no longer matches the `ConversionWebhookServer`'s certificate. Conversion requests fail TLS verification. | A cert-manager rotation that Crossplane has not re-rendered yet. Resolves itself; if it does not, the XRD's own `spec.conversion` is the thing to check. |

From outside the cluster, the same check:

```console
convctl test --xrd xrd.yaml --config config.yaml --live --verify-propagation
```

On a `scope: LegacyCluster` XRD with `claimNames` there are **two** CRDs, and `ConversionPropagated` is True only when both match — a half-propagated pair is how claims silently return wrong data while composites convert fine.

## My conversion keeps disappearing

**Symptom.** Reads at a non-storage version intermittently return the stored object **relabelled but unconverted** — right `apiVersion`, old field layout, HTTP 200, no error anywhere. Minutes later it is fine again. The config still reports `Applied`.

**Check the counter and the condition:**

```console
kubectl get xrdconversionconfig <name> \
  -o jsonpath='{.status.conditions[?(@.type=="PackageManaged")]}{"\n"}'

# from a manager pod, or via your Prometheus
sum by (target) (increase(dco_manager_conversion_reverts_total[24h]))
```

If `PackageManaged` is `True` and the counter is non-zero, this is Crossplane's package establisher. It re-writes every object it established with a full `client.Update` from the package contents — not a Server-Side Apply — on **every** `ConfigurationRevision` reconcile, which the default one-hour `--sync` guarantees. A full `Update` is a replace: `spec.conversion` and this operator's annotations are removed outright. The generated CRD then falls back to `strategy: None` until the operator re-applies, which it does within seconds (it watches XRDs, and self-checks every five minutes besides).

So the exposure is a race of seconds, roughly hourly, per package-managed XRD — and what leaks through it is wrong data with a 200, not an outage. A `Lock` change (any package installed, upgraded or removed anywhere on the cluster) or a Crossplane restart triggers the same thing off-schedule.

**Fix.** Enable the XRD conversion guard (`--enable-xrd-conversion-guard`, chart value `features.crossplane.conversionGuard.enabled`), which re-injects the conversion stanza inside the same admission request, so the window never opens. See [Limitations](../limitations.md#operational) for why patching the generated CRD instead does not help.

Patching `spec.conversion` into the packaged XRD yourself also works, but it means hard-coding a service name, namespace and CA bundle into a portable package.

## The config went `Stale`

The live target's schema no longer matches what the config last validated
against. Under the default `driftPolicy: KeepServingStale` the last known-good
plan **keeps serving** — being `Stale` is a warning, not an outage — and
`Stale=True`, an event, and a phase-transition metric all fire:

```promql
sum by (config_kind, to_phase, reason)
  (rate(dco_manager_phase_transitions_total{to_phase=~"Stale|Failed"}[5m]))
```

Usual causes: someone added or changed a version on the XRD/CRD, or a field's
schema changed under an existing rule. Fix by updating the config to match the
new schema — `convctl diff --config config.yaml --live` reports exactly what
changed in coverage terms. With `driftPolicy: FailClosed` the same drift stops
conversions immediately instead, so treat a `FailClosed` config going `Stale` as
an active incident.

Mid-flight hub promotion is a legitimate, temporary source of drift — see
[Changing the hub version](../configuration/xrdconversionconfig.md#changing-the-hub-version).

## `kubectl apply` of a config is rejected outright

That is this operator's own admission webhook, not the controller, and the
message says which rule failed. Two cases are worth naming:

- **`XRDConversionConfig` rejected on a cluster without Crossplane**, or
  `CRDConversionConfig` rejected when native-CRD support is off: the matching
  feature toggle is disabled. The webhook rejects deliberately, rather than
  accepting an object nothing will ever reconcile. See
  [Feature toggles](../installation.md#feature-toggles).
- **`failed calling webhook ... connection refused`**: the manager isn't
  running or its certificate isn't ready. `admissionWebhook.failurePolicy` is
  `Fail` by default, so a manager outage blocks *applying* this operator's own
  CRs. It does not affect conversions already being served — those are handled
  by the webhook-server pods, which are a separate deployment and a separate
  certificate.

## The manager crash-loops at startup

The most common cause is `features.crossplane.enabled: true` on a cluster where
Crossplane isn't installed. Establishing the watch on Crossplane's
`CompositeResourceDefinition` type is fatal when the type doesn't exist. Set
`features.crossplane.enabled: false` — native-CRD support needs no Crossplane at
all.

## Deleting a config or webhook server hangs

Both kinds are finalizer-gated on purpose, and both report why in a
`DeletionBlocked` condition.

- An `XRDConversionConfig` whose target still serves **more than one version**
  is blocked, because reverting to `strategy: None` would start serving objects
  in the wrong shape to clients on a non-storage version.
- A `ConversionWebhookServer` is blocked while any config resolves to it —
  explicitly via `webhookServerRef` or implicitly as the `default`.

The break-glass annotations, checked live at the moment of the delete reconcile:

```console
kubectl annotate xrdconversionconfig <name> conversion.terasky.com/allow-unsafe-delete=true
kubectl annotate conversionwebhookserver <name> conversion.terasky.com/allow-force-delete=true
```

Read [Deletion safety](../configuration/xrdconversionconfig.md#deletion-safety)
before using either.

## Metrics or alerts look empty

- Scrapes are opt-in: `metrics.serviceMonitor.enabled=true` (plus
  `metrics.prometheusRule.enabled` / `dashboards.enabled` for the shipped alerts
  and dashboard). They're deliberately not capability-detected, so behavior
  doesn't change based on how the chart is rendered.
- Target identity is the label **`target`**, not `xrd`. Queries and alerts
  written against the old `xrd=` label match nothing.
- Manager metrics are on port `8080`, webhook-server metrics on `8443`. If
  `networkPolicy.enabled` is on, `networkPolicy.metrics.allowedPeers` must
  include your Prometheus — see [Metrics trust boundary](../security/metrics.md).

## Related

- [Upgrade runbook](upgrade-runbook.md) — for anything that starts with "after
  we upgraded".
- [HA checklist](ha-checklist.md) — replica counts and disruption budgets.
- [Observability](../observability.md) — every metric, label value, and the
  shipped alert list.
- [Limitations](../limitations.md) — several "why won't it just…" answers are
  documented constraints rather than bugs.
