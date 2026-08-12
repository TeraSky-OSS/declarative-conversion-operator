# Observability

Metric catalog for the manager and ConversionWebhookServer pods, plus
PromQL recipes for registry readiness. Scraped endpoints:

| Component | Port | Path |
|---|---|---|
| Manager | `8080` | `/metrics` |
| Webhook-server | `8443` | `/metrics` |

Enable chart `ServiceMonitor`s with `metrics.serviceMonitor.enabled=true`
(and optional `PrometheusRule` / Grafana dashboard ConfigMaps — see
[Installation](installation.md)). Trust boundary and NetworkPolicy:
[security/metrics.md](security/metrics.md).

**Label note:** target identity uses the Prometheus label `target` (XRD or
CRD resource name). Older scrapes/alerts that filtered on `xrd=` must be
updated.

---

## Webhook-server metrics

Emitted by each ConversionWebhookServer replica (dedicated registry in
`cmd/webhook-server`). Replica identity comes from the scrape target
(`pod` / `instance`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `xrdconv_webhook_conversion_review_duration_seconds` | Histogram | `target`, `direction`, `result` | End-to-end ConversionReview latency |
| `xrdconv_webhook_conversion_review_requests_total` | Counter | `target`, `result` | ConversionReview requests handled |
| `xrdconv_webhook_conversion_objects_total` | Counter | `target`, `from_version`, `to_version`, `result` | Individual objects converted inside reviews |
| `xrdconv_webhook_lossy_conversion_total` | Counter | `target`, `direction` | Conversions on a direction statically known to be lossy |
| `xrdconv_webhook_registry_size` | Gauge | — | Registry entries on this replica (includes error-only placeholders) |
| `xrdconv_webhook_registry_entry_loaded` | Gauge | `target` | `1` if this replica has a compiled, servable plan for that target; `0` if error-only |
| `xrdconv_webhook_registry_last_reload_timestamp_seconds` | Gauge | `target` | Unix time of last successful compile |
| `xrdconv_webhook_registry_reload_total` | Counter | `target`, `result` | Attempted (re)compiles |
| `xrdconv_webhook_registry_compile_errors_total` | Counter | `target`, `reason` | Compile failures that left a stale-or-absent plan in place |
| `xrdconv_webhook_ready` | Gauge | — | `1` after this replica's registry completed initial sync |

### Common label values

- **`result`** (reviews / objects): `success`, `error`, `bad_request`, `not_registered`, `panic`
- **`result`** (reload): `success`, `error`
- **`direction`**: version pair like `v1beta1->v1`, or `hub_to_spoke` / `spoke_to_hub` on lossy counters; `unknown` when undetermined
- **`reason`** (compile): e.g. `XRDNotFound`, `CRDNotFound`, `InvalidRules`, `AnalyzeFailed`, `ValidationErrors`

### Useful PromQL

```promql
# p99 ConversionReview latency by target
histogram_quantile(0.99,
  sum by (le, target) (rate(xrdconv_webhook_conversion_review_duration_seconds_bucket[5m])))

# Error ratio by target
sum by (target) (rate(xrdconv_webhook_conversion_review_requests_total{result="error"}[5m]))
/
sum by (target) (rate(xrdconv_webhook_conversion_review_requests_total[5m]))

# Lossy conversion rate
sum by (target, direction) (rate(xrdconv_webhook_lossy_conversion_total[5m]))
```

---

## Manager metrics

Emitted by the operator manager on controller-runtime's registry
(`:8080/metrics`), in addition to the usual controller-runtime reconcile
and workqueue series.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `xrdconv_manager_analyze_failures_total` | Counter | `config_kind`, `target`, `reason` | Analyze/compile validation failures during config reconcile |
| `xrdconv_manager_apply_duration_seconds` | Histogram | `config_kind`, `target`, `result` | Latency of SSA patches applying conversion webhook config onto the target XRD/CRD |
| `xrdconv_manager_phase_transitions_total` | Counter | `config_kind`, `from_phase`, `to_phase`, `reason` | Config status phase transitions (e.g. Applied→Stale, Applied→Failed) |

- **`config_kind`**: `xrd` or `crd`
- **`to_phase` / `from_phase`**: `Pending`, `Applied`, `Stale`, `Failed`, …
- **`reason`**: e.g. `SchemaDrift`, `Reverted`, `RevertFailed`, `AnalyzeFailed`, `Applied`

```promql
# Analyze failures
sum by (config_kind, target, reason) (rate(xrdconv_manager_analyze_failures_total[5m]))

# Apply p99
histogram_quantile(0.99,
  sum by (le, config_kind, target) (rate(xrdconv_manager_apply_duration_seconds_bucket[5m])))

# Transitions into Stale or Failed
sum by (config_kind, to_phase, reason)
  (rate(xrdconv_manager_phase_transitions_total{to_phase=~"Stale|Failed"}[5m]))
```

---

## Watch-map metric

When a secondary watch map function fails to `List` related configs (API
timeout, etc.), the failure is logged and counted instead of looking like
a legitimate empty result. Configs still periodically self-reconcile
(`RequeueAfter`), so a transient List miss recovers without pod restart.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `xrdconv_watch_map_list_errors_total` | Counter | `map_func` | Watch-mapping List failures |

---

## Registry readiness (per replica)

Each ConversionWebhookServer replica owns an in-memory registry of
compiled plans. Desired assignment on the CWS object
(`status.assignedConfigs`) is **not** proof that a given replica has
loaded those configs — that state is local to each pod.

Example: confirm every **ready** webhook-server pod has loaded
target `xfoos.example.org` (empty result = healthy):

```promql
(xrdconv_webhook_ready == 1)
  unless on (pod)
(xrdconv_webhook_registry_entry_loaded{target="xfoos.example.org"} == 1)
```

Use `instance` instead of `pod` if that is the stable scrape identity in
your Prometheus config.

Compare desired assignment count on the CWS status with live load:

```promql
# per-replica loaded (servable) entries
count by (pod) (xrdconv_webhook_registry_entry_loaded == 1)
```

`ConversionWebhookServer.status.assignedConfigs` remains the cluster-level
**desired** set computed by the shared resolver. Use it together with the
per-pod gauges above — not as a substitute for them.

---

## Alerts and dashboards

The chart ships:

- **PrometheusRule** (`metrics.prometheusRule.enabled`) — compile errors,
  fleet/replica not-ready, high latency, lossy rate, error ratio, manager
  analyze failures, and Stale/Failed phase transitions. Expressions are
  unit-tested under `hack/prometheus/` (`make test-prometheus`).
- **Grafana dashboard ConfigMap** (`dashboards.enabled`) — labeled
  `grafana_dashboard: "1"` for the Grafana sidecar; JSON lives at
  `charts/declarative-conversion-operator/files/dashboards/conversion-overview.json`.

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
