# Observability

Metric catalog for the manager and ConversionWebhookServer pods, plus
PromQL recipes for registry readiness. Scraped endpoints:

| Component | Port | Path |
|---|---|---|
| Manager | `8080` | `/metrics` |
| Webhook-server | `8443` | `/metrics` |

Enable chart `ServiceMonitor`s with `metrics.serviceMonitor.enabled=true`
(and optional `PrometheusRule` / Grafana dashboard ConfigMaps — see
[Installation](installation.md)). `make dev-up` turns those on automatically
and installs kube-prometheus-stack with anonymous Grafana
(`hack/dev-monitoring-values.yaml`) unless you pass `DEV_MONITORING=false`.
Trust boundary and NetworkPolicy:
[security/metrics.md](security/metrics.md).

**Label note:** target identity uses the Prometheus label `target` (XRD or
CRD resource name). Older scrapes/alerts that filtered on `xrd=` must be
updated.

**Metric prefix:** all series use `dco_` (declarative-conversion-operator).
The pre-1.0 `xrdconv_` prefix is retired — update scrapes, alerts, and
dashboards that still reference it.

---

## Webhook-server metrics

Emitted by each ConversionWebhookServer replica (dedicated registry in
`cmd/webhook-server`). Replica identity comes from the scrape target
(`pod` / `instance`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `dco_webhook_conversion_review_duration_seconds` | Histogram | `target`, `direction`, `result` | End-to-end ConversionReview latency |
| `dco_webhook_conversion_review_requests_total` | Counter | `target`, `result` | ConversionReview requests handled |
| `dco_webhook_conversion_objects_total` | Counter | `target`, `from_version`, `to_version`, `route`, `result` | Individual objects converted inside reviews. `route` classifies the conversion by shape — `hub_to_spoke`, `spoke_to_hub`, `spoke_to_spoke`, `identity` — which the version pair cannot: which version is the hub is a per-target fact, not a label. It is a function of labels the series already carries, so it adds no cardinality |
| `dco_webhook_conversion_object_duration_seconds` | Histogram | `target`, `direction`, `result` | Per-object conversion latency. Prefer this over the review histogram for anything sliced by `direction` — see the note below |
| `dco_webhook_conversion_batch_size` | Histogram | `target` | Objects carried by one ConversionReview. The input for sizing `--max-request-bytes` |
| `dco_webhook_conversion_panics_total` | Counter | `target` | Panics recovered while serving a review. Always a bug in this operator; alert on any increase |
| `dco_webhook_lossy_conversion_total` | Counter | `target`, `direction` | Conversions on a direction statically known to be lossy. A spoke-to-spoke conversion passes through the hub and is counted once for each of its two hops that is lossy |
| `dco_webhook_registry_size` | Gauge | — | Registry entries on this replica (includes error-only placeholders) |
| `dco_webhook_registry_entry_loaded` | Gauge | `target` | `1` if this replica has a compiled, servable plan for that target; `0` if error-only |
| `dco_webhook_registry_last_reload_timestamp_seconds` | Gauge | `target` | Unix time of last successful compile |
| `dco_webhook_registry_reload_total` | Counter | `target`, `result` | Attempted (re)compiles |
| `dco_webhook_registry_compile_errors_total` | Counter | `target`, `reason` | Compile failures that left a stale-or-absent plan in place |
| `dco_webhook_ready` | Gauge | — | `1` after this replica's registry completed initial sync |
| `dco_webhook_initial_sync_duration_seconds` | Gauge | — | Seconds this replica spent compiling every assigned plan before reporting ready. Written once; `0` on a replica still cold, which `dco_webhook_ready` disambiguates |
| `dco_webhook_initial_sync_targets` | Gauge | — | Configs walked during that cold start. Divide the duration by it for a per-target cost |

### `direction` on the two latency histograms

A ConversionReview is batched by the version the apiserver *wants*, not by
the version each object currently *is*, so one request can legitimately
carry objects converting `v1->v3` and `v2->v3` at the same time. There is no
honest single `direction` for such a request, and
`dco_webhook_conversion_review_duration_seconds` labels it `mixed` rather
than picking one.

That makes the review histogram the right measure of **request** latency and
the wrong measure of per-direction cost. Use
`dco_webhook_conversion_object_duration_seconds` for the latter: its
`direction` is always exactly the conversion that was timed. Both are
exported; neither replaces the other.

Sizing `--max-request-bytes` from
`dco_webhook_conversion_batch_size` is more reliable than guessing from
object size alone, because the limit applies to the encoded review and
therefore scales with batch size as well as with object size.

### Common label values

- **`result`** (reviews / objects): `success`, `error`, `bad_request`, `not_registered`, `panic`
- **`result`** (reload): `success`, `error`
- **`direction`**: version pair like `v1beta1->v1`, or `hub_to_spoke` / `spoke_to_hub` on lossy counters; `unknown` when undetermined
- **`reason`** (compile): e.g. `XRDNotFound`, `CRDNotFound`, `InvalidRules`, `AnalyzeFailed`, `ValidationErrors`

### Useful PromQL

```promql
# p99 ConversionReview latency by target
histogram_quantile(0.99,
  sum by (le, target) (rate(dco_webhook_conversion_review_duration_seconds_bucket[5m])))

# Objects converted per second (fleet)
sum(rate(dco_webhook_conversion_objects_total[5m]))

# ConversionReview RPCs per second (fleet)
sum(rate(dco_webhook_conversion_review_requests_total[5m]))

# Objects /s by target and result
sum by (target, result) (rate(dco_webhook_conversion_objects_total[5m]))

# Error ratio by target
sum by (target) (rate(dco_webhook_conversion_review_requests_total{result="error"}[5m]))
/
sum by (target) (rate(dco_webhook_conversion_review_requests_total[5m]))

# Lossy conversion rate. A spoke-to-spoke conversion is two hops and is
# counted once per lossy hop, so this can exceed the object rate.
sum by (target, direction) (rate(dco_webhook_lossy_conversion_total[5m]))

# Share of traffic by route shape. This is what says whether direct
# spoke-to-spoke plans would be worth building for your cluster — see
# Capacity planning for why the shipped answer is "no".
sum by (route) (rate(dco_webhook_conversion_objects_total[5m]))
  / ignoring(route) group_left
sum(rate(dco_webhook_conversion_objects_total[5m]))
```

---

## Manager metrics

Emitted by the operator manager on controller-runtime's registry
(`:8080/metrics`), in addition to the usual controller-runtime reconcile
and workqueue series.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `dco_manager_analyze_failures_total` | Counter | `config_kind`, `target`, `reason` | Analyze/compile validation failures during config reconcile |
| `dco_manager_apply_duration_seconds` | Histogram | `config_kind`, `target`, `result` | Latency of SSA patches applying conversion webhook config onto the target XRD/CRD |
| `dco_manager_phase_transitions_total` | Counter | `config_kind`, `from_phase`, `to_phase`, `reason` | Config status phase transitions (e.g. Applied→Stale, Applied→Failed) |
| `dco_manager_conversion_reverts_total` | Counter | `config_kind`, `target` | A previously-applied `spec.conversion` was found **missing** from the target — an out-of-band overwrite. On an XRD owned by a Crossplane `ConfigurationRevision` this is the package establisher's non-SSA `client.Update`; see the `PackageManaged` condition on the config. |
| `dco_manager_conversion_propagated` | Gauge | `target` | `1` when every CRD Crossplane generates from this applied XRD carries the conversion webhook, `0` when it does not. This is the series the `ConversionAppliedButNotPropagated` alert reads: a gauge, because the question is about a specific target's **current** state, which no rate over counters can answer — those aggregate away which apply they are describing, so one old propagation observation would suppress the alert for a later apply that never propagated. |
| `dco_manager_propagation_lag_seconds` | Histogram | `target` | Time from patching `spec.conversion` onto an XRD until Crossplane's generated CRD was observed carrying it. Observed **once per transition into propagated**, not on every reconcile. XRD targets only — a `CRDConversionConfig`'s target *is* the CRD, so there is nothing to propagate. |

- **`config_kind`**: `xrd` or `crd`
- **`to_phase` / `from_phase`**: `Pending`, `Applied`, `Stale`, `Failed`, …
- **`reason`**: e.g. `SchemaDrift`, `Reverted`, `RevertFailed`, `AnalyzeFailed`, `Applied`

```promql
# Analyze failures
sum by (config_kind, target, reason) (rate(dco_manager_analyze_failures_total[5m]))

# Apply p99
histogram_quantile(0.99,
  sum by (le, config_kind, target) (rate(dco_manager_apply_duration_seconds_bucket[5m])))

# Transitions into Stale or Failed
sum by (config_kind, to_phase, reason)
  (rate(dco_manager_phase_transitions_total{to_phase=~"Stale|Failed"}[5m]))

# Conversion stripped out of band (package establisher, typically hourly)
sum by (config_kind, target) (increase(dco_manager_conversion_reverts_total[1h]))
```

A non-zero `dco_manager_conversion_reverts_total` on a target whose config
reports `PackageManaged=True` is the signature of Crossplane's package
establisher: it re-writes every established object with a full
`client.Update` from the package contents on each revision reconcile, which
the default one-hour `--sync` guarantees. The operator re-applies within
seconds, but reads during that window return stored objects **relabelled
and unconverted, with no error**. The `ConversionRevertedOutOfBand` alert
covers it; enabling the XRD conversion guard closes the window entirely.

---

## Controller health: the leading indicator

controller-runtime exports workqueue and reconcile metrics for **every**
controller in both processes. They are not this operator's own metrics, and
they are the ones that move first.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `workqueue_depth` | Gauge | `name`, `controller`, `priority` | Items waiting to be reconciled |
| `workqueue_adds_total` | Counter | `name`, `controller` | Enqueues |
| `workqueue_queue_duration_seconds` | Histogram | `name`, `controller` | How long an item waited before a worker picked it up |
| `workqueue_work_duration_seconds` | Histogram | `name`, `controller` | How long one reconcile took |
| `workqueue_retries_total` | Counter | `name`, `controller` | Requeues after a failed reconcile |
| `controller_runtime_reconcile_total` | Counter | `controller`, `result` | Reconciles, by outcome |
| `controller_runtime_reconcile_errors_total` | Counter | `controller` | Reconciles that returned an error |
| `controller_runtime_reconcile_time_seconds` | Histogram | `controller` | Reconcile latency |
| `controller_runtime_active_workers` | Gauge | `controller` | Workers currently busy |
| `controller_runtime_max_concurrent_reconciles` | Gauge | `controller` | The ceiling `--max-concurrent-reconciles` sets |

Both processes export them. The manager serves controller-runtime's
registry directly; the webhook-server serves its own dedicated registry
*and* controller-runtime's from one handler, so a replica's registry
reconcile loop is visible on the same panels as the manager's controllers.

### Why depth is the metric to watch

The causal chain runs in one direction, and every link is slower to notice
than the one before it:

```
workqueue_depth rises
  → reconciles are queued longer than they take to run
    → a config's status.phase goes Stale
      → the XRD keeps an old spec.conversion, or never gets one
        → ConversionPropagated lags, and reads come back unconverted
```

By the time `dco_manager_conversion_propagated` drops to 0 the backlog has
already been there for a while. Depth is the only signal in that chain that
moves *before* anything is wrong for a user, which is what makes the panels
worth having rather than decorative.

What matters is a depth that **stays** up. A bulk apply of two hundred
configs legitimately spikes the queue and then drains it; that is the
shape the alert's `for:` exists to tolerate.

```promql
# Backlog, per controller, in both processes
sum by (job, controller) (workqueue_depth)

# Is the backlog "lots of work" or "slow work"? High adds + flat depth is the
# first; low adds + rising depth is the second.
sum by (job, controller) (rate(workqueue_adds_total[5m]))

# Reconcile latency
histogram_quantile(0.99, sum by (le, job, controller) (rate(workqueue_work_duration_seconds_bucket[5m])))

# Saturation: workers busy against the configured ceiling
sum by (controller) (controller_runtime_active_workers)
  / sum by (controller) (controller_runtime_max_concurrent_reconciles)
```

### The lever

`--max-concurrent-reconciles` (Helm: `manager.maxConcurrentReconciles`)
sets how many objects each controller reconciles at once. It defaults to
1 — controller-runtime's own default — and is worth raising when depth is
persistently non-zero *and* work duration is not the problem. The cost is
apiserver QPS, which is why it is not raised by default.

Correctness does not depend on it: controller-runtime guarantees a given
object key is never reconciled by two workers simultaneously, and nothing
in these reconcile paths shares mutable state across keys.

---

## Watch-map metric

When a secondary watch map function fails to `List` related configs (API
timeout, etc.), the failure is logged and counted instead of looking like
a legitimate empty result. Configs still periodically self-reconcile
(`RequeueAfter`), so a transient List miss recovers without pod restart.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `dco_watch_map_list_errors_total` | Counter | `map_func` | Watch-mapping List failures |

---

## Registry readiness (per replica)

Each ConversionWebhookServer replica owns an in-memory registry of
compiled plans. Desired assignment on the CWS object
(`status.assignedConfigs`) is **not** proof that a given replica has
loaded those configs — that state is local to each pod.

Example: confirm every **ready** webhook-server pod has loaded
target `xfoos.example.org` (empty result = healthy):

```promql
(dco_webhook_ready == 1)
  unless on (pod)
(dco_webhook_registry_entry_loaded{target="xfoos.example.org"} == 1)
```

Use `instance` instead of `pod` if that is the stable scrape identity in
your Prometheus config.

Compare desired assignment count on the CWS status with live load:

```promql
# per-replica loaded (servable) entries
count by (pod) (dco_webhook_registry_entry_loaded == 1)
```

`ConversionWebhookServer.status.assignedConfigs` remains the cluster-level
**desired** set computed by the shared resolver. `status.servedTargets` is
the reported counterpart — the intersection of what every live replica
publishes it can serve, with `status.reportingReplicas` saying how many fed
it. Between them they answer "is this instance ready for this target?"
without a scrape; the per-pod gauges above remain the finer-grained answer
to *which* replica is missing one.

### Cold start

The plain endpoint (`/healthz`, `/readyz`, `/metrics`) listens *before* the
informer cache syncs, so a replica that is still compiling is visibly alive
rather than indistinguishable from a hung process. Its `/readyz` stays
`503` and the conversion endpoint does not listen at all until the registry
is populated.

```promql
# Slowest cold start in the fleet — size startupProbe.failureThreshold from this
max(dco_webhook_initial_sync_duration_seconds)

# Per-target cold-start cost for your schemas
dco_webhook_initial_sync_duration_seconds / dco_webhook_initial_sync_targets
```

See [Capacity planning](operations/capacity.md#cold-start-how-long-before-a-replica-can-serve)
for the measured curve and the reasoning behind the default budget.

---

## Alerts and dashboards

The chart ships:

- **PrometheusRule** (`metrics.prometheusRule.enabled`) — compile errors,
  fleet/replica not-ready, high latency, lossy rate, error ratio, manager
  analyze failures, Stale/Failed phase transitions, and the two
  controller-health alerts (`ControllerWorkqueueBacklog`,
  `ControllerReconcileErrors`) whose thresholds are
  `metrics.prometheusRule.workqueueDepthThreshold`,
  `.workqueueBacklogFor` and `.reconcileErrorRateThreshold`. Expressions
  are unit-tested under `hack/prometheus/` (`make test-prometheus`).
- **Grafana dashboard ConfigMaps** (`dashboards.enabled`) — labeled
  `grafana_dashboard: "1"` for the Grafana sidecar; JSON under
  `charts/declarative-conversion-operator/files/dashboards/`:
  - [`conversion-overview.json`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/charts/declarative-conversion-operator/files/dashboards/conversion-overview.json) — fleet-wide overview, plus a **Controller health** row (workqueue depth, add rate, work duration p50/p99, reconcile error rate) for both processes
  - [`conversion-target-detail.json`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/charts/declarative-conversion-operator/files/dashboards/conversion-target-detail.json) — one XRD/CRD via the `target` dropdown (`target` label)
  - [`conversion-stability.json`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/charts/declarative-conversion-operator/files/dashboards/conversion-stability.json) — platform stability deep dive: conversions/s, failure rate, latency, registry, manager control plane, and webhook/manager pod CPU/memory/restarts (kubelet cAdvisor + kube-state-metrics)

  All three share the Grafana tag `conversion` so the dashboard header links
  between them. Resource panels on the stability dashboard need kubelet
  cAdvisor and kube-state-metrics (kube-prometheus-stack provides both);
  conversion/manager panels only need this chart's `ServiceMonitor`s.
  Both scrapes expose `go_goroutines` / `process_*`: the webhook-server's
  `/metrics` gathers controller-runtime's registry alongside its own, and
  that registry carries the Go and process collectors. cAdvisor is still
  the better source for container-level memory, because it measures the
  same working set the kernel enforces a limit against.

---

## Debug endpoint (break-glass)

`GET /debug/registry` on the plain-HTTP port still returns a JSON snapshot
of the local registry. Prefer metrics for alerting and routine checks;
use the debug endpoint only when you already have pod exec / port-forward
access.

---

## Optional tracing

OpenTelemetry tracing on the ConversionReview path is **optional and
default-off**. Enable it per ConversionWebhookServer with `spec.extraArgs`:

```yaml
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: default
spec:
  extraArgs:
    - --otel-exporter-otlp-endpoint=otel-collector.observability.svc:4317
    - --otel-trace-sample-ratio=0.1
    # Optional: disable TLS only for trusted in-cluster collectors
    # - --otel-exporter-otlp-insecure=true
```

When the endpoint flag is empty (the default), the webhook-server uses a
no-op tracer — no exporter is started. When set, sampled spans cover
`ConversionReview` receipt → per-object `Convert` → response and export
via OTLP/gRPC (suitable for Jaeger/Tempo).

---

## Related

- Symptom-driven use of these metrics: [operations/troubleshooting.md](operations/troubleshooting.md)
- Custom-metric HPA example: [operations/hpa-custom-metrics.md](operations/hpa-custom-metrics.md)
- Metrics trust boundary and NetworkPolicy: [security/metrics.md](security/metrics.md)
- ConversionWebhookServer status fields: [configuration/conversionwebhookserver.md](configuration/conversionwebhookserver.md)
