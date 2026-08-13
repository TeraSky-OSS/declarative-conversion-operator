# Limitations

This page is deliberately blunt about what the operator does *not* do today, so you can decide up front whether it fits your use case.

## Scope

- **Crossplane XRDs and plain native CRDs, nothing else.** `pkg/engine` (the actual conversion logic) is intentionally kept agnostic of any particular resource type — it only depends on standard Kubernetes OpenAPI schema types and a small `SchemaSource` interface — with two adapters implementing it today: `pkg/xrdadapter` (Crossplane XRDs) and `pkg/crdadapter` (native CRDs). Both are independently toggleable — see [Installation: Feature toggles](installation.md#feature-toggles).
- **Crossplane v2 (`apiextensions.crossplane.io/v2`) is the targeted API.** The operator reads/patches `spec.conversion` the same way on v1 and v2 XRDs, but it's only tested against the current `v2` API in CI.
- **The hub version must be the target resource's storage version** (`referenceable: true` on an XRD, `storage: true` on a native CRD). This is enforced as a hard validation error — you can't designate an arbitrary spoke as the conversion hub.
- **One config per target resource.** Enforced structurally by a unique field index plus the admission webhook, not a runtime lock — this is a design constraint, not a current gap, but it does mean you can't split one resource's conversion logic across multiple `XRDConversionConfig`/`CRDConversionConfig` objects.

## Rule-authoring constraints

- **`forEach` nesting is capped at depth 2.** A `forEach` may wrap another `forEach` for arrays-of-arrays; a third level is rejected at compile and admission time (not silently truncated). This bounds the CRD schema recursion the engine has to reason about. See [For Each](strategies/for-each.md).
- **`forEach` requires strict positional correspondence** between the hub and spoke arrays — same length, same order. If both the hub and spoke item paths are present on the input as arrays of different lengths, conversion fails with a hard runtime error (it does not silently coerce to the source length). When the destination path is absent, the output array is sized from the source alone.
- **Free-form maps (`additionalProperties: true`) and preserve-unknown-fields subtrees are opaque, all-or-nothing units.** A rule must claim the whole subtree; the engine does not reason about individual keys inside one. If you need field-level control over part of a free-form map, model those fields explicitly in the schema instead.
- **`oneOf` / `anyOf` / `allOf` / `$ref` nodes are treated opaquely.** Analyze does not flatten through those JSON Schema constructs. An uncovered leaf sitting inside one is reported with the construct name in the diagnostic (not a generic "uncovered field" message). Transforming across a `oneOf` branch is out of scope.
- **`arrayToMapByKey` requires unique key values.** A duplicate `keyField` value across array elements is a runtime conversion error, not a silent overwrite.
- **Some strategies can never be statically proven lossless**, because the engine can't reason about arbitrary regexes, templates, JSON Patch documents, or CEL at compile time: `jsonPatch`, `scalarToFields`/`fieldsToScalar` (unless you set `losslessOverride: true` and back that claim up with `convctl test` against representative data), `cel` (always lossy — no `losslessOverride`), and `mapToArrayByKey`/round-tripping through a sorted array (always lossy — see [Array ⇄ Map by Key](strategies/array-map-key.md)).

## Operational

- **CRD schema changes require a manual step on Helm upgrade.** CRDs are installed once at `helm install` and never touched by `helm upgrade`/`helm uninstall` (Helm's own recommended convention for CRD-heavy charts) — see [Upgrading](installation.md#upgrading).
- **Per-pod webhook-server state isn't surfaced back into `ConversionWebhookServer.status`.** `status.assignedConfigs` reflects *desired* assignment computed by the shared resolver, not confirmation that every replica has actually finished compiling and loading a given config — that's a deliberate trade-off to avoid the operator's reconcile loop depending on network calls to webhook-server pods. Check each pod's `/debug/registry` endpoint or metrics for real per-pod state.
- **Spoke-to-spoke conversions always route through the hub** — two `Convert` calls, never a direct spoke-to-spoke path. This keeps compilation cost linear in the number of spoke versions. A 1000-element `forEach` spoke-to-spoke convert is ~2.3× a single hop and still under 1 ms ([Capacity planning](operations/capacity.md)); shortcut plans are not implemented.
- **No cross-cluster or multi-region coordination.** Every `ConversionWebhookServer` instance and every operator replica assumes a single Kubernetes cluster.

## Scale envelopes

Microbenchmarks for compile time vs schema size and `Convert` vs array length
live in [`pkg/engine/*_bench_test.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/tree/main/pkg/engine)
and are summarized in [Capacity planning](operations/capacity.md). Extremely
large or deeply nested schemas beyond the published 1000-leaf / 1000-element
points are still unvalidated against a live apiserver. Re-run `make bench`
locally; `make test-e2e-load` posts synthetic ConversionReview batches at a
live webhook-server.

If something here blocks you, please [open an issue](https://github.com/terasky-oss/declarative-conversion-operator/issues) — several of these are natural extension points the design was deliberately seamed for (see [Roadmap](roadmap.md)), not fundamental barriers.
