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

## Memory: the manager

The manager is an informer cache with some code attached, so its resident size
is a property of **what it watches**, not of how much work it does.

It no longer watches Secrets at all. A CA bundle is one key of one Secret per
`ConversionWebhookServer`, read on the cold path of an apply; caching that cost
a cluster-wide Secret informer, and Secrets are typically the largest object
class in a cluster by total bytes. Reads go through to the API server, which
also removes the staleness window on certificate rotation. The Deployments,
Services, HPAs and PDBs it owns are label-scoped to
`app.kubernetes.io/managed-by=declarative-conversion-operator`.

The startup log line `informer caches scoped` states this on a running process,
so it can be confirmed without reading the source.

## Memory: the webhook-server

Every replica holds the schemas it might have to convert against. Before this,
that was every CRD and every XRD on the cluster, unscoped, with `managedFields`
and the kubectl last-applied annotation included — none of which the engine
reads.

Two levers, one of which is on by default:

- A cache transform, always applied, strips `managedFields` and
  `kubectl.kubernetes.io/last-applied-configuration` before an object enters
  the store. This is what produced the 50% above, and it needs no
  configuration.
- `ConversionWebhookServer.spec.cacheSelector` narrows *which* objects are held
  at all — and it now narrows the schema informers as well as the config
  informers, so the target XRDs and CRDs have to carry the label too.

### Why the transform does not prune schemas

Stripping `managedFields` is safe because nothing reads them. Pruning the
schemas of versions no config targets would save more — schemas are the bulk of
a CRD object — and is deliberately not done. The cache is built before any
config has been read, configs are added and retargeted at runtime, and
controller-runtime informers are not re-scopable. A version pruned at startup
would be silently missing when a config later named it, and a conversion
against a truncated schema does not fail: it returns wrong data. The memory is
the cheaper side of that trade. See [Limitations](../limitations.md).

### Sizing

Scale a replica's memory with the **number and size of cached schemas**, not
with conversion QPS. Replica count multiplies whatever each replica holds;
every replica is symmetric and caches the same set. The chart's default limit
is 256 MiB, which the cluster above fits with room to spare after this change
and did not before. A cluster with substantially more or larger CRDs should
raise it or set a `cacheSelector`.

There are three terms, and they are not the same size:

| Term | What it scales with | Measured |
|---|---|---|
| **Informer cache** | every CRD and XRD the replica watches, schemas included | the 121 MiB above, for 300 two-version 200-property CRDs — **the dominant term** |
| **Compiled registry** | number of targets × their schema size | ~18 KiB per target (see below) |
| **Cold-start transient** | allocation churn while compiling, not anything retained | 8–13× the steady registry, depending on fleet size — **what an OOM kill is decided against** |

#### Bytes per compiled plan

`make bench-mem`, `BenchmarkCompiledPlanRetained` in `pkg/engine`. Live heap
either side of building N plans and holding them all — not `-benchmem`'s
`B/op`, which counts the garbage a compile produces as well as what survives
it:

| Leaves (per version) | Retained per plan | Churned per compile | Ratio |
|---|---:|---:|---:|
| 10 | 2.5 KiB | 47 KiB | 19× |
| 100 | 21 KiB | 467 KiB | 22× |
| 1000 | 234 KiB | 4.7 MiB | 20× |

Retained cost is linear in leaf count, about **240 bytes per leaf**. The
number that matters operationally is the third column: **a compile churns
roughly twenty times what it keeps.**

#### Registry footprint

`BenchmarkRegistryRetained` in `internal/webhookserver`, over a fleet of
two-version targets of 50 leaves each with one `FieldRename` rule per leaf —
so each target carries one compiled plan:

| Targets | Retained per target | Registry total |
|---|---:|---:|
| 10 | 83 KiB | 0.8 MiB |
| 100 | 18.6 KiB | 1.8 MiB |
| 1000 | 18.4 KiB | 18 MiB |

The 10-target row is fixed per-replica overhead divided by ten, not a real
per-target cost; from a hundred targets up the figure is flat at ~18 KiB.
**A thousand targets is 18 MiB of registry** — small enough that the registry
is never the reason a replica needs a bigger limit.

#### Peak versus steady state

`BenchmarkInitialSyncPeak`, sampling live heap every 2 ms through the cold
start:

| Targets | Steady registry | Peak during sync | Ratio |
|---|---:|---:|---:|
| 100 | 1.8 MiB | 23–26 MiB | ~13× |
| 1000 | 18 MiB | 140 MiB | ~8× |

The ratio falls as the fleet grows because the fixed per-replica overhead
stops dominating, not because the transient gets cheaper in absolute terms.

This is the finding worth acting on. The peak is not memory the replica
needs; it is memory the garbage collector has not reclaimed yet, because
with the default `GOGC` the heap is allowed to double the live set before a
collection — and a cold start allocates twenty times what it keeps, as fast
as it can, across every core.

The kernel enforcing a container memory limit does not wait for the GC.
**`GOMEMLIMIT` is what makes the GC aware of the same number**, so the
operator sets it on every webhook-server container at 90% of
`spec.resources.limits.memory` whenever a limit is set. With it, the same
thousand-target run peaks at 61 MiB instead of 140 MiB, taking 1.6 s instead
of 0.45 s — which is the trade a memory limit is asking for. Set
`GOMEMLIMIT` yourself in `spec.extraEnv` to override the derived value; the
operator leaves an explicit one alone.

!!! warning "`GOMEMLIMIT` is a soft target, not a cap"
    It makes the collector work harder as the heap approaches the number —
    it cannot free memory that is still live, and it does not cover
    allocations outside the Go runtime. Against a **transient** peak, which
    is what the table above measures, that is exactly the right lever. Against
    a **live** working set larger than the limit it does nothing except
    collect continuously, and the container is OOM-killed anyway, now with a
    CPU burn in front of it.

    So `GOMEMLIMIT` is not a substitute for sizing the limit. Size it for the
    live set — registry plus informer cache, the two terms below — and leave
    headroom on top; `GOMEMLIMIT` is what stops the cold-start transient from
    needing headroom of its own.

#### Worked example

A cluster with **1000 targets averaging 200 leaves per version**, on the
chart's default 256 MiB limit:

- Registry: 200 leaves × 240 B ≈ 48 KiB per plan, ×1000 ≈ **48 MiB**.
- Informer cache: the schemas those targets live in. Extrapolating the
  measured 121 MiB for 300 two-version 200-property CRDs gives roughly
  **400 MiB** — this term alone blows the default limit.
- Cold-start transient: without `GOMEMLIMIT`, several hundred MiB on top.

So: **set `cacheSelector`, or raise the limit to ~1 GiB.** The registry is
not the problem at any plausible scale; the informer cache is, and it is the
one term this operator can only narrow, never shrink.

Note which term `GOMEMLIMIT` can and cannot help with here, because this
example is the case that makes the distinction concrete: the ~400 MiB
informer cache is **live**, so no GC setting brings it under a 256 MiB
limit — only `cacheSelector` or a bigger limit will. What `GOMEMLIMIT` does
is stop the cold-start transient from adding several hundred MiB on top of
whatever limit you land on.

The chart's 256 MiB default was reviewed against these numbers and left
alone. It is right for the cluster it is a default for — a few dozen
targets, a few hundred CRDs — and raising it would silently raise the
scheduling floor for every install to serve the minority that need it. What
was wrong was that the Go runtime had no idea the limit existed, so the
cold-start transient was sized by `GOGC` alone. `GOMEMLIMIT` tells it.

### How these numbers were taken

`hack/measure-cache-memory.sh` builds two real images from two real commits,
installs each in turn into the same loaded cluster, and reads
`workingSetBytes` from the kubelet Summary API — the same number for both
sides, and the one a container memory limit is enforced against. A single
sample is not reproducible (it depends on where in its GC cycle the process
is), so it takes seven samples 20 s apart after a 180 s settle.

Cluster: kind v1.35, Crossplane 2.x, **2000 Secrets of ~3 KiB** across 20
namespaces and **300 CRDs** carrying 200-property schemas across two versions
each.

| Process | Before | After | Change |
|---|---:|---:|---:|
| manager | 151 MiB | 151 MiB | ~0% |
| webhook-server | 240 MiB | 121 MiB | **−50%** |

Within a single run the spread across the seven samples was under 0.1 MiB.
*Between* runs it is much wider — the same baseline build on a
freshly-installed cluster came back 207 MiB — so compare a before and an after
from **one** invocation of the script, which is what it is built to do, and do
not compare a number here against one taken any other way.

> [!IMPORTANT]
> **The manager row does not measure what it looks like it measures.** At
> ~3 KiB per Secret, 2000 Secrets are only ~8 MB — noise next to the 300 CRD
> schemas the manager also caches and which this change does not touch. So the
> flat result says the Secret informer was not the dominant term *on this
> cluster*; it does not say removing it was worthless. The saving is linear in
> total Secret bytes, which is exactly the quantity that varies most between
> clusters and is unbounded on a busy one.
>
> To measure it on a cluster where it matters, raise the payload:
>
> ```bash
> BASE_REF=main hack/measure-cache-memory.sh --secrets 2000 --secret-bytes 65536
> ```
>
> The blast-radius argument stands on its own regardless: the ClusterRole has
> to grant Secret access cluster-wide because the namespace is not knowable
> ahead of time, and the process no longer holds the contents. See
> [RBAC blast radius](../security/rbac.md).

The webhook-server row is the unambiguous one, and it is the transform rather
than the selector doing the work — no selector was set in this run.

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

## Cold start: how long before a replica can serve

A replica does not answer conversions until its registry holds a compiled
plan for every target assigned to it. That startup pass — `InitialSync` — is
the whole of the cold start, and it is what a `startupProbe` has to be sized
against.

`BenchmarkInitialSync` in `internal/webhookserver/initialsync_bench_test.go`
walks N targets, each a two-version XRD of 50 leaves per version with one
`FieldRename` rule per leaf, and compiles them all:

| Targets | Serial | Parallel (GOMAXPROCS=24) | Speed-up |
|---|---:|---:|---:|
| 10 | 12 ms | 8 ms | 1.5× |
| 100 | 73 ms | 50 ms | 1.5× |
| 1000 | 825 ms | 391 ms | 2.1× |

Compilation is CPU-bound and independent per target, so `InitialSync` runs a
bounded worker pool — `GOMAXPROCS` by default, `--initial-sync-workers` to
override. The benchmark's client is a fake backed by one mutex, so its
API-read term is more serialised than a real informer cache and the speed-up
above is a floor, not a ceiling.

**A thousand targets is under a second of compile.** The cold-start budget is
therefore dominated not by this operator but by the informer cache sync in
front of it: a replica watching every CRD and XRD on a large cluster spends
most of its startup waiting for those LISTs. That is what
`spec.cacheSelector` reduces, and it is why the default budget is minutes
rather than seconds.

### The startup probe

`ConversionWebhookServer.spec.startupProbe` renders a `startupProbe` on the
webhook-server container; `periodSeconds × failureThreshold` is the budget,
defaulting to 5 × 60, i.e. five minutes.

It polls **`/readyz`**, which is what makes the budget real. The plain
endpoint carrying `/healthz`, `/readyz` and `/metrics` comes up *before* the
registry sync, so `/healthz` answers within milliseconds of process start; a
`startupProbe` pointed at it would succeed immediately and bound nothing.
`/readyz` stays false until the initial sync completes, so
`periodSeconds × failureThreshold` is a deadline on the sync itself.

The kubelet runs neither of the other two probes while a `startupProbe` is
in flight, so a slow sync is not simultaneously fighting the liveness
probe's own 3 × 10 s. Once the probe succeeds, liveness (`/healthz`) and
readiness (`/readyz`) take over as usual.

Two things the deadline buys, beyond not crash-looping a slow replica:

- **The initial sync retries infrastructure failures without a limit** — a
  failed read of a target, a failed server list — because a watch-driven
  reconciler will not necessarily re-deliver an event for what failed. That
  is the right behaviour for a transient failure and the wrong one for a
  permanent one, and the `startupProbe` is what distinguishes them.
- **Without it a wedged replica is invisible.** It would stay
  liveness-healthy and never ready: out of the Service, never restarted,
  showing up only as a gap in `readyReplicas`.

Erring long is deliberate. An over-tight threshold turns a slow start into a
crash loop; an over-long one only delays the restart of a pod that is not
taking traffic anyway.

### Measuring your own

Two metrics and a log line, published once per replica at the moment it
reports ready:

```promql
# Cold start, per replica
dco_webhook_initial_sync_duration_seconds

# Targets that cold start compiled
dco_webhook_initial_sync_targets

# Per-target cost for your schemas
dco_webhook_initial_sync_duration_seconds / dco_webhook_initial_sync_targets
```

```
registry synced, marking replica ready  serverName=default targets=812 workers=8 elapsed=1.412s
```

Set `failureThreshold` from the slowest cold start you observe, with room to
spare — it is a deadline on exactly the interval this metric measures. The
plain HTTP endpoint (`/healthz`, `/readyz`, `/metrics`) comes up *before*
the cache sync, so a replica that is still cold is visibly alive and
scrapeable rather than indistinguishable from a hung process, and the
`startupProbe` is polling a live listener rather than collecting
connection-refused.

### Reporting ready anyway after a timeout

Considered and deliberately not implemented. A `--registry-ready-timeout`
that let a replica join the Service with a partially-populated registry
would have it answer ConversionReviews for targets it has not compiled yet
with a failure — which the apiserver turns into a failed write on a
resource that has nothing to do with the slow config. An unavailable replica
degrades throughput; a half-loaded one corrupts the answer. The
`startupProbe` is the supported lever, and the current fail-closed ordering
stands.

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

Defaults are a smoke size (4 CRDs × 5 CRs). Override with env vars. The
100×100 and 100×1000 figures below are **local** capacity runs on a
workstation; the envelope CI exercises unattended is the nightly one
described under [The nightly scale run](#the-nightly-scale-run).

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

### The nightly scale run

`.github/workflows/scale.yml` runs `hack/e2e-scale.sh` on a schedule at
**300 CRDs × 20 objects** (6,000 objects, 900 served versions), publishes
`scale-result.json` as a 90-day artifact, renders it into the job summary,
and compares it against the previous successful run.

That is where the numbers in this section come from from now on. A local
run on a workstation is still the right tool for investigating a change;
the scheduled run is what notices one nobody was looking for.

**Regression detection is relative, never absolute.** Absolute timings on a
hosted runner vary by a factor of two between runs for reasons that have
nothing to do with this code, so a threshold tight enough to catch a real
regression would fire constantly. The check fails when a measurement
exceeds a configurable multiple — 1.5× by default — of the *same
measurement in the previous run at the same envelope, under the same report
schema*. Below a noise floor (20 ms, 1 s, 32 MiB depending on the unit) a
ratio is not treated as a signal: a p50 that moved from 2 ms to 4 ms is a
2× regression by arithmetic and scheduler noise by every other reading.

Two things are checked absolutely rather than as a trend, because for them
zero is the only acceptable value: any Get/List error, and a run that
issued no requests at all — which would otherwise report zero of everything
and look like a pass.

A failure names the measurement. The summary's comparison table marks the
offending row **REGRESSED** and the job log repeats it as
`FAIL: listV1 p50: 90.0 ms -> 190.0 ms (2.11x, threshold 1.50x)`, so the
first question ("what got slower?") is answered without downloading
anything.

The artifact carries more than latency: `hack/scale-observe.py` merges in
the webhook-server's **cold-start time**
(`dco_webhook_initial_sync_duration_seconds`) and its **loaded working
set** (from the kubelet Summary API), so the two numbers this page's memory
and cold-start sections are about are trended by the same job. Both are
gated against the previous run alongside the latency figures.

The working set is a single sample taken once the replicas are Ready again
after the restart, so it is the **steady state with the fleet loaded, not
the transient peak** — the peak happens before readiness, where nothing is
sampling. The peak-versus-steady table above is what measures that, from
`make bench-mem`.

#### Why 300 CRDs, and not the 1000 the proposal asked for

The target in [the phase proposal](../proposals/next-phases.md) is 1000
CRDs, on the reasoning that it is roughly the CRD count of a mature
Crossplane cluster. The scheduled run does not reach it yet, and
configuring an aspirational number that always fails would be worse than
publishing a smaller one that always runs.

A standard GitHub-hosted runner is 4 vCPU and 16 GiB, hosting a
single-node kind cluster whose apiserver, etcd, the operator and the
webhook-server replicas all share those four cores. Two terms make CRD
count, rather than object count, the binding constraint there:

- **Applying CRDs is apiserver-CPU-bound, not IO-bound.** Each `CustomResourceDefinition`
  write makes the apiserver rebuild parts of its aggregated OpenAPI
  document and re-establish the resource's handler. On the workstation runs
  below that cost is invisible next to object creation; on four shared
  cores it is not.
- **Every CRD is watched three times over** — by the apiserver, by the
  operator, and by each webhook-server replica — and each replica also
  holds its schemas resident. At 1000 CRDs that is the informer footprint
  the [sizing section](#worked-example) puts at several hundred MiB per
  replica, against a 16 GiB box already running a control plane.

300 × 20 completes in roughly 25 minutes end to end and has headroom
against the job's 75-minute timeout, which is what "reliable" has to mean
for something that runs unattended. The envelope is a `workflow_dispatch`
input precisely so the ceiling can be probed upward with evidence rather
than moved by assertion: run it at 500, then 750, and raise the default
when a higher number has run clean several times. A run at a different
envelope publishes its numbers and skips the comparison, so probing cannot
produce a false regression.

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
| `--result-json` / `RESULT_JSON` | unset | Write the run's measurements as JSON, and merge in the cluster-side observations |

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
