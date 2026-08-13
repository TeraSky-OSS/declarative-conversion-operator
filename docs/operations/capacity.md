# Capacity planning

This page mixes operational defaults with measured envelopes from the Phase 9
benchmark suite. Absolute times vary by CPU; the shapes (linear in leaf count
and array length) are what to plan around. Re-run locally with `make bench`.

Numbers below are from `go test -bench=. -benchmem -benchtime=300ms ./pkg/engine/`
on an Intel Core Ultra 9 285HX (WSL2). Treat them as order-of-magnitude, not
SLOs. The CI `Microbenchmarks (smoke)` job only proves the benchmarks compile
and run; it does not gate on wall-clock.

## Compile vs schema size

`Compile` is linear in the number of schema leaves when each leaf has a
`FieldRename` rule (the common case). It is **not** on the ConversionReview
hot path — webhook-server compiles once per config change / pod start.

| Leaves | ns/op | B/op | allocs/op | ≈ time |
|---|---:|---:|---:|---:|
| 10 | 30k | 48 KiB | 214 | 0.03 ms |
| 100 | 317k | 467 KiB | 1.8k | 0.32 ms |
| 1000 | 3.0M | 4.7 MiB | 17k | 3.0 ms |

A 1000-leaf schema compiling in ~3 ms is well inside a reconcile budget.
Hundreds of targets compiling on pod start is still seconds, not minutes.

## Convert vs array length

Hot-path `Convert` for the ops whose cost scales with element count. Direction
is hub→spoke. Cost is linear in `n`.

| Op | n=10 | n=100 | n=1000 |
|---|---:|---:|---:|
| `forEach` (2 nested FieldRenames) | 2.9 µs | 29 µs | 332 µs |
| `arrayToMapByKey` | 4.1 µs | 34 µs | 428 µs |
| `mapToArrayByKey` | 3.2 µs | 37 µs | 443 µs |

A 1000-element array is still sub-millisecond per object. Pathological arrays
(tens of thousands of elements) would show up as data-shape problems, not
"need more replicas."

## JSONPatch marshal cost

Each `jsonPatch` op applies an RFC 6902 patch to a marshaled copy of the
input, then unmarshals and copies touched paths into the output. Convert
caches that marshaled input on the per-request context so a second op does
not re-marshal the same object (`encoding/json` already pools its encoder
state).

| JSONPatch ops | before (ns/op) | after (ns/op) | allocs after |
|---|---:|---:|---:|
| 1 (tiny object) | 5.2k | 5.5k | 64 |
| 3 (tiny object) | 17k | 13.5k | 170 |
| 3 (100-leaf object) | — | 330k | 4.6k |

The 1-op path is unchanged (marshal once either way). The 3-op path dropped
~20% and is now ~2.5× one op instead of ~3.3×. Remaining cost is
apply+unmarshal per op, which is inherent to json-patch. Prefer fewer, larger
patch documents over many tiny ones.

## Spoke-to-spoke vs hub hop

Router always goes spoke A → hub → spoke B (`O(N)` compiled plans, never
pairwise). For a 1000-element `forEach` object:

| Route | ns/op | vs hub→spoke |
|---|---:|---|
| hub → spoke | 328k | 1.0× |
| spoke → spoke | 765k | 2.3× |

Spoke-to-spoke is essentially two Converts. Even in this worst-case array
shape it stays under 1 ms. The apiserver stores at the hub version, so
spoke-to-spoke is rare in production. Direct shortcut plans are not worth
breaking compile-time `O(N)` for — see issue #80.

## cacheSelector watch and memory reduction

`ConversionWebhookServer.spec.cacheSelector` is implemented (Phase 6): webhook
replicas pass `--cache-label-selector` and controller-runtime scopes
XRDConversionConfig / CRDConversionConfig informers with `cache.ByObject`.
Non-matching objects are never listed or cached.

At 10,000 synthetic configs with a selector matching 1% (`tenant=a`):

| Scope | Watches (objects held) | Memory proxy (8 KiB/object) |
|---|---:|---:|
| Unscoped (default) | 10,000 | 80 MiB |
| `matchLabels: {tenant: a}` | 100 | 0.8 MiB |

That is a 99% reduction in both watch count and cache RAM. Use a selector per
tenant (or per team) when one cluster holds many configs but each webhook
instance only serves a slice.

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

## Remaining gaps

- Synthetic large-batch ConversionReview load e2e
  ([issue #79](https://github.com/TeraSky-OSS/declarative-conversion-operator/issues/79)).
- Registry copy-on-write cost at 100+ entries
  ([issue #78](https://github.com/TeraSky-OSS/declarative-conversion-operator/issues/78)).

## Related

- [HA checklist](ha-checklist.md) — replica floors and PDBs.
- [HPA on conversion QPS](hpa-custom-metrics.md) — scaling on traffic, not CPU.
- [Observability](../observability.md) — full metric catalog and alerts.
