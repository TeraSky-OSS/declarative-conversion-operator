# Capacity planning

This page is a starting point, not a benchmark report. Phase 9 will add
measured compile-time and ConversionReview latency numbers; until then, treat
the guidance below as operational defaults that have held up in development and
CI, and validate them against your own traffic with the metrics in
[Observability](../observability.md).

## What actually consumes capacity

| Workload | Who pays | Scales with |
|---|---|---|
| ConversionReview serving | Webhook-server pods | Objects read/written at a non-storage version × spoke count × rule complexity |
| Plan compile (registry load) | Webhook-server pods, once per config change / pod start | Schema size × rule count — not on the request path after load |
| Config reconcile | Manager | Config + target XRD/CRD churn — not request traffic |
| `convctl test --live` | The machine running `convctl` | Live object count; use `--concurrency` |

The manager is rarely the bottleneck. Size the webhook-server fleet for
admission-path latency and availability; size the manager for reconcile
backlog only if you run hundreds of configs.

## Starting points

- **Webhook-server:** chart default is 2 replicas. Stay at 2 until p99
  ConversionReview latency or error ratio rises under load, then add replicas
  (or enable CPU HPA / custom-metric HPA — see
  [HPA on conversion QPS](hpa-custom-metrics.md)).
- **Manager:** 1 replica with leader election is enough for most clusters. Add a
  second only if you need faster failover for GitOps applies (admission
  `failurePolicy: Fail`).
- **Resources:** keep the chart defaults until Prometheus shows sustained CPU
  throttling or OOM kills on webhook-server pods. Conversion work is typically
  CPU-bound and memory-light once plans are loaded.

## What to watch

```promql
# Serving latency (per target)
histogram_quantile(0.99,
  sum by (le, target) (rate(dco_webhook_conversion_duration_seconds_bucket[5m])))

# Error ratio
sum(rate(dco_webhook_conversion_review_requests_total{result!="success"}[5m]))
  /
sum(rate(dco_webhook_conversion_review_requests_total[5m]))

# Compile cost after a config change (should be a spike, not a plateau)
rate(dco_webhook_registry_compile_duration_seconds_sum[5m])
```

If latency climbs while CPU is idle, look for oversized ConversionReview
batches or pathological array sizes under `forEach` / `arrayToMapByKey` —
those show up as data-shape problems, not "need more replicas."

## Known gaps (Phase 9)

- No published compile-time vs schema-size curve yet
  ([issue #75](https://github.com/terasky-oss/declarative-conversion-operator/issues/75)).
- No synthetic large-batch ConversionReview load e2e yet
  ([issue #79](https://github.com/terasky-oss/declarative-conversion-operator/issues/79)).
- Extremely large/deep schemas and huge `forEach` arrays are called out as
  unvalidated in [Limitations](../limitations.md).

When those land, this page will cite the numbers instead of defaults.

## Related

- [HA checklist](ha-checklist.md) — replica floors and PDBs.
- [HPA on conversion QPS](hpa-custom-metrics.md) — scaling on traffic, not CPU.
- [Observability](../observability.md) — full metric catalog and alerts.
