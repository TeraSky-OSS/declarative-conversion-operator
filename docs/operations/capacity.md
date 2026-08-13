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
spoke-to-spoke is rare in production. Direct shortcut plans were evaluated
and rejected: they would push compilation toward `O(N²)` spoke pairs for a
gain that does not show up under the 1s p99 ConversionReview alert. Hub-and-
spoke remains the only routing mode.

## cacheSelector watch and memory reduction

`ConversionWebhookServer.spec.cacheSelector` is implemented (Phase 6): webhook
replicas pass `--cache-label-selector` and controller-runtime scopes
XRDConversionConfig / CRDConversionConfig informers with `cache.ByObject`.
Non-matching objects are never listed or stored. There is still one watch
per GVK; the selector cuts the **store size** (and therefore cache RAM), not
the watch count.

At 10,000 synthetic configs with a selector matching 1% (`tenant=a`):

| Scope | Objects the informer would hold |
|---|---:|
| Unscoped (default) | 10,000 |
| `matchLabels: {tenant: a}` | 100 |

That is a 99% reduction in cached objects. Memory scales with that store, so
the same ratio applies to RAM. Use a selector per tenant (or per team) when
one cluster holds many configs but each webhook instance only serves a slice.

## Registry copy-on-write at 100+ entries

`Registry.Set` copies the whole map of pointers and atomically swaps it so
`Get` stays lock-free. At realistic scale:

| Entries | Set ns/op | Get ns/op |
|---|---:|---:|
| 10 | 0.7k | — |
| 100 | 4.5k | — |
| 1000 | 40k | 7 (serial) / 0.6 (parallel) |

A 4.5 µs copy on config churn (seconds to minutes apart) is noise next to
compile time (~3 ms for 1000 leaves). The hot path is `Get`, which is a
single atomic load. The current copy-on-write map is adequate; a more
complex persistent structure is not justified.

## ConversionReview load (kind)

`make test-e2e-load` stands up a native-CRD kind cluster and POSTs synthetic
`ConversionReview` batches at the live webhook-server (`FieldRename` +
`ToAnnotation` + `DefaultValue` + `Delete` on the Gadget fixture). Numbers
from one run on the same workstation as the microbenchmarks (kindest/node
v1.35, port-forward to `default-webhook-server`):

| Objects / review | Extra pad / object | p50 | p99 | Reviews/s | Objects/s | Errors |
|---|---:|---:|---:|---:|---:|---:|
| 1 | 0 | 7.8 ms | 11.0 ms | 125 | 125 | 0 |
| 10 | 0 | 7.9 ms | 12.8 ms | 117 | 1.2k | 0 |
| 50 | 0 | 8.7 ms | 10.6 ms | 113 | 5.7k | 0 |
| 10 | 8 KiB | 11.5 ms | 13.9 ms | 87 | 867 | 0 |

Batch size barely moves p50 (the TLS + HTTP overhead dominates this fixture).
An 8 KiB annotation pad adds ~3 ms. Error rate was zero. These are
client-observed times through `kubectl port-forward`, so they include that
hop; in-cluster apiserver→webhook is typically a bit faster.

The 1s p99 alert in [Observability](../observability.md) is more than an
order of magnitude above this envelope for small objects. Re-run with
`make test-e2e-load` (or `KEEP_CLUSTER=1`) after changing the hot path.

## Per-strategy Convert cost

`BenchmarkConvert_PerStrategy` in `pkg/engine/strategy_bench_test.go` times
hub→spoke `Convert` for every built-in strategy on a tiny object (one field
or a 2-element array). Same workstation as the tables above
(`go test -bench=BenchmarkConvert_PerStrategy -benchtime=300ms`). Use this
to compare strategies, not as an SLO — real requests also pay TLS, JSON
codec, and apiserver overhead (see the load and cluster-scale sections).

| Strategy | ns/op | B/op | allocs/op | ≈ time |
|---|---:|---:|---:|---:|
| DefaultValue / Constant / Delete | ~80–88 | 96 | 2 | 0.08 µs |
| FieldRename | 258 | 384 | 3 | 0.26 µs |
| NumericScale / Duration / ObjectToScalar | ~328–330 | 384–392 | 3–4 | 0.33 µs |
| EnumRemap / TypeCoerce / Quantity | ~345–390 | 392–400 | 4 | 0.37 µs |
| MapToFields / ListJoin / ListSplit | ~391–472 | 384–536 | 3–10 | 0.4 µs |
| ScalarToObject / MapKeyRename / FieldsToMap | ~499–698 | 720 | 5 | 0.6 µs |
| SingletonArrayToObject | 708 | 720 | 5 | 0.71 µs |
| ScalarToFields | 755 | 560 | 9 | 0.76 µs |
| ObjectToSingletonArray | 950 | 760 | 7 | 0.95 µs |
| FromLabel / ToAnnotation / FromAnnotation / ToLabel | ~1.0–1.1 µs | ~1.1 KiB | 9–10 | 1.0 µs |
| FieldsToScalar | 1.3 µs | 969 | 14 | 1.3 µs |
| ArrayToMapByKey / MapToArrayByKey | 2.4–3.0 µs | ~3.4 KiB | 21–30 | 2.7 µs |
| ForEach (2 nested FieldRenames, tiny array) | 3.1 µs | 3.6 KiB | 29 | 3.1 µs |
| JSONPatch (1 op) | 3.9 µs | 1.7 KiB | 44 | 3.9 µs |
| CEL | 4.0 µs | 2.6 KiB | 43 | 4.0 µs |

Cheap path-copy strategies stay in the hundreds of nanoseconds. JSONPatch and
CEL are ~15× FieldRename on a tiny object (marshal / CEL program eval), still
well under a millisecond. Array-shaped ops (`forEach`, `arrayToMapByKey`,
`mapToArrayByKey`) scale with element count — see Convert vs array length
above for n=10/100/1000.

## Cluster-scale Get/List (kind)

`make test-e2e-scale` (`hack/e2e-scale.sh` → `cmd/scalegen`) stands up a
native-CRD kind cluster, then **generates** a fleet of CRDs and drives real
apiserver Get/List (which invoke the conversion webhook) in parallel:

- Each CRD has **3 versions** (`v3` storage hub, `v1`/`v2` spokes).
- Each spoke conversion has **3–10 strategies**, assigned so **all 29**
  built-in strategies appear across the fleet (`2 × targets × strategies-max`
  must be ≥ 29).
- Instances are created at `v1`; Get/List run at both spoke versions so the
  apiserver converts hub↔spoke on every call.
- Every CRD is in the `widgets` category: `kubectl get widgets -n dco-scale`
  lists the whole fleet. Re-apply with `--reset` if older CRDs lack the
  category.

Defaults are a smoke size (4 CRDs × 5 CRs). Override with env vars — this is
**not** in the CI e2e matrix; 100×100 and 100×1000 are local capacity runs.

```console
# smoke (default)
make test-e2e-scale

# 100 CRDs × 100 CRs × 3 versions, 32 parallel Get/List workers
TARGETS=100 INSTANCES=100 PARALLEL=32 make test-e2e-scale

# 100 CRDs × 1000 CRs (100k objects), 60 parallel workers
TARGETS=100 INSTANCES=1000 PARALLEL=60 make test-e2e-scale

# skip kind teardown while iterating; --reset drops old generated CRDs
KEEP_CLUSTER=1 TARGETS=20 INSTANCES=20 make test-e2e-scale
go run ./cmd/scalegen --reset --targets 20 --instances 20 --parallel 16 --qps 100 --namespace dco-scale
```

### 100×100 on a single-node kind cluster

Same workstation as the microbenchmarks (Intel Core Ultra 9 285HX, WSL2,
kindest/node v1.35, one control-plane node). 100 CRDs × 3 versions, 3–10
strategies per spoke (all 29 used across the fleet), 100 instances created
at `v1`, then parallel Get/List at both spokes (`PARALLEL=32`, QPS 100 /
burst 200). Create of 10,000 objects took **1m38s**. Zero conversion errors.

| Op | Calls | Objects / call | p50 | p99 | max | Errors |
|---|---:|---:|---:|---:|---:|---:|
| list v1 | 300 | 100 | 299 ms | 400 ms | 410 ms | 0 |
| list v2 | 300 | 100 | 300 ms | 392 ms | 402 ms | 0 |
| get v1 | 10,000 | 1 | 320 ms | 336 ms | 388 ms | 0 |
| get v2 | 10,000 | 1 | 320 ms | 340 ms | 411 ms | 0 |

A List of 100 objects is the same ~300 ms p50 as a single Get. Engine
`Convert` on these fixtures is microseconds; the kind apiserver, etcd, and
TLS hop dominate. Spoke v1 and v2 are indistinguishable. The 1s p99
ConversionReview alert is ~3× this envelope. Re-run with
`TARGETS=100 INSTANCES=100 PARALLEL=32 make test-e2e-scale` after changing
the serving path.

### 100×1000 on a single-node kind cluster

Latest local run, same workstation and kind topology as 100×100 (Intel Core
Ultra 9 285HX, WSL2, kindest/node v1.35, one control-plane node). 100 CRDs
× 3 versions, 3–10 strategies per spoke (all 29 used across the fleet),
1000 instances created at `v1` (**100,000** objects), then parallel
Get/List at both spokes (`PARALLEL=60`, QPS 100 / burst 200). Create of
100,000 objects took **16m38s**. Zero conversion errors.

| Op | Calls | Objects / call | p50 | p99 | max | Errors |
|---|---:|---:|---:|---:|---:|---:|
| list v1 | 300 | 1000 | 613 ms | 897 ms | 995 ms | 0 |
| list v2 | 300 | 1000 | 560 ms | 779 ms | 810 ms | 0 |
| get v1 | 100,000 | 1 | 600 ms | 617 ms | 1.034 s | 0 |
| get v2 | 100,000 | 1 | 0 s | 625 ms | 932 ms | 0 |

Create scaled ~linearly with object count (~10× objects, ~10× wall time vs
100×100). List/Get p50 only about doubled despite 10× instances per CRD, so
the kind apiserver + etcd + TLS hop still dominate engine `Convert`. List
v1 p99 (897 ms) and Get v1 max (1.034 s) now sit on the 1s p99
ConversionReview alert — at this density the single-node kind control plane
is the bottleneck, not conversion. Re-run with
`TARGETS=100 INSTANCES=1000 PARALLEL=60 make test-e2e-scale` after changing
the serving path.

| Flag / env | Default | Meaning |
|---|---|---|
| `--targets` / `TARGETS` | 4 | Number of CRDs (each with 3 versions) |
| `--instances` / `INSTANCES` | 5 | CRs created per CRD |
| `--strategies-min` / `STRATEGIES_MIN` | 3 | Min strategies per spoke |
| `--strategies-max` / `STRATEGIES_MAX` | 10 | Max strategies per spoke |
| `--parallel` / `PARALLEL` | 8 | Concurrent create/get/list workers |
| `--qps` / `QPS` | 100 | client-go QPS (client-go's default of 5 throttles 10k creates) |
| `--burst` / `BURST` | 200 | client-go burst |
| `--reset` / `RESET` | true in `e2e-scale.sh` | Delete previously generated CRDs in this group before apply |
| `--seed` / `SEED` | 1 | Strategy assignment RNG |
| `--list-repeats` / `LIST_REPEATS` | 3 | List calls per CRD per spoke version |
| `--get-repeats` / `GET_REPEATS` | 1 | Get calls per instance per spoke version |
| `--dry-run` | false | Print strategy coverage only (no cluster) |

Native CRDs are used on purpose: they exercise the same `pkg/engine` +
webhook-server path as XRDs without requiring Crossplane. Times above include
apiserver + etcd + conversion, not just `Convert`.

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

## Related

- [HA checklist](ha-checklist.md) — replica floors and PDBs.
- [HPA on conversion QPS](hpa-custom-metrics.md) — scaling on traffic, not CPU.
- [Observability](../observability.md) — full metric catalog and alerts.
