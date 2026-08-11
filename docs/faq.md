# FAQ

## General

### Why not just hand-write a conversion webhook?

You can — this operator doesn't do anything a hand-written webhook couldn't. What it removes is the boilerplate and the ways to get it wrong: standing up an HTTP server, wiring TLS via cert-manager, patching `spec.conversion` at the right time (and not before), and reasoning field-by-field about whether a mapping actually loses data in either direction. With `XRDConversionConfig`/`CRDConversionConfig`, that becomes a declarative rule list that's validated (offline via `convctl`, and again at `kubectl apply` time) before it ever reaches a live schema, and the webhook runtime itself is shared infrastructure you configure once, not code you maintain per resource.

### Does this work without Crossplane?

Yes. `CRDConversionConfig` targets plain native `CustomResourceDefinition`s and has no Crossplane dependency at all — see [CRDConversionConfig](configuration/crdconversionconfig.md). `XRDConversionConfig` is the Crossplane-specific one, for XRDs. Both are enabled by default and independently toggleable; see the next question.

### Does the operator crash if Crossplane isn't installed?

Not by default configuration, but it matters *which* configuration. Watching Crossplane's `CompositeResourceDefinition` GVK is fatal at manager startup if that CRD doesn't exist on the cluster — so on a Crossplane-less cluster you must set `features.crossplane.enabled: false` (`--enable-xrd-support=false` on the binaries). This is the one setting that's required, not just optional, on such a cluster; see [Installation: Feature toggles](installation.md#feature-toggles). With it set, the manager never registers that watch, and `CRDConversionConfig`/native-CRD support works normally. The [CRD-only e2e job](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/hack/e2e-test-crd-only.sh) runs exactly this configuration against a cluster with no Crossplane installed at all, as a standing regression check.

### Can I use both XRDs and native CRDs at once?

Yes — `features.crossplane.enabled` and `features.nativeCRD.enabled` are independent; the default is both `true`. A single operator install can reconcile `XRDConversionConfig` and `CRDConversionConfig` objects side by side, served by the same (or different) `ConversionWebhookServer` instances.

## Configuration and safety

### What's a "hub" version, and why does one have to be picked?

The hub is the version every rule is expressed relative to, and it must equal the XRD's `referenceable: true` version (or the CRD's `storage: true` version) — Crossplane's/Kubernetes' own storage version. A spoke-to-spoke conversion is always served as two hops through the hub (spoke → hub → spoke), never a direct spoke-to-spoke mapping. This keeps the number of rule sets you write at O(N) for N versions instead of O(N²), and anchors "is this lossless" to what's actually persisted, not to some other served-but-not-stored representation.

### How do I promote a different version to be the hub?

See [XRDConversionConfig: Changing the hub version](configuration/xrdconversionconfig.md#changing-the-hub-version) for the full runbook. Short version: add the new version as an ordinary spoke first, then update the config's `hubVersion` and flip the schema's storage/referenceable flag — in either order. The `DriftPolicy: KeepServingStale` default means a brief mismatch between the two doesn't cause an outage; the controller just keeps serving the last-known-good plan until both sides agree again.

### If I make a mistake in my config, does it break my existing conversion?

No — not by default. Every reconcile fully re-validates before touching anything, and the controller **only ever patches `spec.conversion` after every gate passes**. If a change would introduce an error, an uncovered field, or an unacknowledged lossy rule, the config goes `Invalid` and the schema is never touched — whatever conversion webhook configuration was already applied (if any) keeps running untouched. See [XRDConversionConfig: Ordering](configuration/xrdconversionconfig.md#ordering-nothing-touches-the-xrd-until-every-gate-passes).

### What if the XRD/CRD's schema changes after my config was already `Applied`?

This is "drift," checked on every reconcile by rehashing the live schema plus the config's rules. A clean re-validation self-heals silently. A failing one goes loudly `Stale` (condition, event, metric) but — under the default `KeepServingStale` policy — keeps serving the last known-good compiled plan rather than un-patching a working webhook. `DriftPolicy: FailClosed` is available if you'd rather stop serving conversions immediately on drift than risk a plan validated against a schema that no longer exists.

### Why did my rule get rejected as "lossy"?

The engine computes lossiness per rule, per direction, from fixed logic specific to each strategy (e.g. `Delete` is always lossy on the direction the field exists; `fieldRename` is always lossless both ways). Any lossy direction needs `acknowledgeLossy: true` on that rule — this is a deliberate fail-closed default: an unacknowledged lossy mapping is treated as a bug in the config, not something to silently accept. Run `convctl analyze` or `convctl validate` to see exactly which rule and direction triggered it before ever applying the config.

### What happens to a field I forgot to write a rule for?

If it's not structurally identical between hub and spoke, it's an **uncovered-field error** — the config is rejected, not silently accepted with that field dropped. This is the concrete meaning of "unknown means assume lossy": `unmappedFieldPolicy: Warn` (with a required `unmappedFieldReason`) downgrades this to a warning if you deliberately want to allow it.

### What about fields that aren't in my XRD's schema at all — like Crossplane's `status.conditions`?

That's a different case from the one above, and it's handled automatically — for fields nested inside a container your schema already declares (`spec`, `status`), which is what `status.conditions` and `spec.crossplane` actually are. Crossplane injects that standard shape into every generated CRD version, but those specific fields are never part of the XRD's own authored `openAPIV3Schema` — the schema the controller actually compiles against. Since the engine has no schema information about them at all, on either side, it can't reason about their shape the way it does for a field you declared but forgot to write a rule for. Rather than silently dropping them (the bug this behavior fixes) or forcing you to hand-declare Crossplane's own boilerplate in every version just to satisfy the compiler, the engine copies any such nested field straight through, unchanged, on every conversion. You don't need a rule for it, and it never shows up as an uncovered-field error. This deliberately does *not* extend to a brand-new top-level sibling of `spec`/`status` your schema never mentions at all — that's a structurally different, much rarer situation, and outside what this behavior covers.

### Can two configs target the same XRD or CRD?

No — this is blocked structurally by a unique index plus the admission webhook, not resolved by a race at runtime. Creating a second `XRDConversionConfig`/`CRDConversionConfig` for a target that already has one is rejected at `kubectl apply` time.

### Is it safe to delete an `XRDConversionConfig`/`CRDConversionConfig`?

Deletion runs a finalizer-gated "safe revert," not an immediate removal. If the target still has more than one served version, reverting to `strategy: None` would risk serving objects in the wrong shape to a client still on a non-storage version — so deletion is **blocked** unless you set the explicit break-glass annotation (`conversion.terasky.com/allow-unsafe-delete: "true"`), checked live at the moment of the delete reconcile. See [Deletion safety](configuration/xrdconversionconfig.md#deletion-safety).

## Testing

### How do I test a config before it ever touches a cluster?

`convctl validate`/`analyze`/`test` run the exact same `pkg/engine` code path the controller and webhook server use, entirely offline against local YAML files — no cluster required. `convctl test` in particular round-trips every sample through every served-version pair and reports pass/loss/fail per path; see the [CLI Reference](cli.md).

### Can I test a config against objects that already exist in my cluster, not just fixtures?

Yes — `convctl test --live` fetches every existing instance of the target type at its hub/storage version (so it works even before any conversion webhook is wired up) and runs the same round-trip checks against them. This is the tool to run before applying a new or changed config as a pre-upgrade check. See [Pre-upgrade checks](cli.md#pre-upgrade-checks-testing-against-everything-that-already-exists).

### Does this integrate with CI?

`convctl test` supports `--output json` for scripting and `--output junit` for CI systems with JUnit test-result reporting (GitHub Actions, GitLab, Jenkins), plus `--output-file` to write the report to a specific path. Exit codes are deliberately distinct for "a test failed" (`1`) versus "the tool was used wrong" (`2`), so CI can tell the two apart.

## Operations and scaling

### Can I run more than one `ConversionWebhookServer`?

Yes — it's designed for this. Each instance is cluster-scoped, deployable, and independently scalable (Deployment, Service, cert-manager Certificate, optional HPA/PodDisruptionBudget). The chart installs exactly one, marked `default: true`; you can create more for tenancy or scale-out and assign specific configs to them via `webhookServerRef`. Every replica of every instance is symmetric and self-sufficient — no leader election, no shared state — so horizontal scaling is just adding pods.

### What happens if a single config fails to compile on a webhook-server replica?

It's non-fatal to the pod. That specific target keeps serving whatever plan was last good for it; the failure shows up in metrics and `/debug/registry`, not as a crash-loop or a de-ready of the whole replica. A single bad config never takes down conversion for every other target that replica serves.

### What's the performance impact of adding a conversion webhook to the admission path?

Each `ConversionWebhookServer` replica keeps its compiled plans in a lock-free, copy-on-write in-memory registry (`atomic.Pointer` swap on update, no locking on the read path), so a conversion request never touches a database, another service, or even a lock — just an in-memory map lookup and the plan's own execution. `pkg/engine`'s `Convert` step itself is a single pass over pre-split path segments and pre-decoded values (enum tables, parsed JSON Patch documents), not a re-parse of anything per request.

## Getting help

### I hit a case none of the built-in strategies handle. Now what?

`jsonPatch` is the escape hatch — an arbitrary RFC 6902 patch list, always treated as lossy unless you explicitly set `losslessOverride: true` (the engine can't statically verify a patch is its own inverse). If you find yourself reaching for it often for the same shape of problem, [open an issue](https://github.com/terasky-oss/declarative-conversion-operator/issues) — real-world API migration patterns are what shapes which strategy gets added next; see the [Roadmap](roadmap.md).

### Where do I report a bug or ask a question?

[GitHub Issues](https://github.com/terasky-oss/declarative-conversion-operator/issues) on the repository.
