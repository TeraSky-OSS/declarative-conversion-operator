# declarative-conversion-operator

> [!WARNING]
> **Alpha, under active development.** APIs (the CRDs, the Helm chart's values, and CLI flags) may still change without notice, and this hasn't yet been run in production. Expect rough edges; issues and feedback are welcome.

A Kubernetes operator that lets admins declare field-level conversions between [Crossplane](https://crossplane.io) XRD (`CompositeResourceDefinition`) versions, and between plain native Kubernetes CRD versions, using built-in strategies — no hand-written conversion webhook required.

Both Crossplane XRDs and native CRDs support multiple version schemas, but wiring up a real Kubernetes conversion webhook to convert between them today means writing and deploying custom Go code. This operator replaces that with a declarative custom resource — `XRDConversionConfig` for Crossplane XRDs, `CRDConversionConfig` for plain native CRDs — sharing the exact same rule vocabulary: pick a hub version, describe how each spoke version's fields map to it using named strategies (`fieldRename`, `scalarToObject`, `toAnnotation`, `enumRemap`, …), and the operator validates the mapping, compiles it, and — only once everything is verified healthy — patches the target resource to route conversion requests to a shared, horizontally-scalable webhook server.

Both are independently toggleable (`--enable-xrd-support` / `--enable-crd-support`, or `features.crossplane.enabled` / `features.nativeCRD.enabled` in the Helm chart) — disable XRD support on clusters without Crossplane installed, since Crossplane isn't otherwise a hard dependency.

## Why

- **Declarative, not hand-written.** No Go code, no bespoke webhook deployment per XRD.
- **Conservative by default.** Any conversion the engine can't prove is lossless is rejected unless explicitly acknowledged (`acknowledgeLossy: true`).
- **Safe by construction.** The operator never patches a live XRD until the config is validated, the XRD is healthy, and the assigned webhook server is confirmed ready — and never reverts a working conversion setup on deletion if doing so would strand clients on a non-storage version.
- **Fast where it matters.** The webhook server precompiles every rule into a resolved-path execution plan and serves ConversionReview requests from an in-memory, per-replica registry — no network calls, no re-parsing, in the API admission critical path.
- **Testable offline.** The `convctl` CLI runs the exact same conversion engine against local YAML files and a directory of samples, before anything touches a cluster.

## Architecture

Three CRDs, API group `terasky.com/v1alpha1`:

- **`XRDConversionConfig`** (cluster-scoped) — one per target Crossplane XRD. Declares a hub version and, per spoke version, a list of declarative conversion rules.
- **`CRDConversionConfig`** (cluster-scoped) — the same thing for a plain native `CustomResourceDefinition`, sharing the exact same rule vocabulary (`ConversionRule`/`Strategy`) and controller ordering; only the target resource type differs.
- **`ConversionWebhookServer`** (cluster-scoped) — a deployable, independently scalable instance of the shared conversion webhook runtime (its own Deployment, cert-manager `Certificate`, `Service`, `HorizontalPodAutoscaler`, `PodDisruptionBudget`). Serves both `XRDConversionConfig`- and `CRDConversionConfig`-backed conversions. The Helm chart creates one by default, marked `default: true`; create more for scale-out or tenant isolation.

```
                    ┌─────────────────────────┐
  kubectl apply ──► │   XRDConversionConfig    │
                    └────────────┬─────────────┘
                                 │ validated by
                                 ▼
                    ┌─────────────────────────┐        ┌──────────────────────────┐
                    │   operator (cmd/manager) │──SSA──►│  target XRD              │
                    │  pkg/engine.Analyze()    │ patch  │  spec.conversion.webhook │
                    └────────────┬─────────────┘        └────────────┬─────────────┘
                                 │ resolves assignment                │ ConversionReview
                                 ▼                                    ▼
                    ┌─────────────────────────┐        ┌──────────────────────────┐
                    │  ConversionWebhookServer │───────►│ cmd/webhook-server pods  │
                    │  (Deployment/Service/…)  │  owns  │  in-memory plan registry │
                    └─────────────────────────┘        └──────────────────────────┘
```

Everything that actually performs a conversion — the controller's validation, the webhook server's hot path, and the CLI — goes through the same `pkg/engine` package, which is deliberately kept agnostic of Crossplane: it depends only on standard Kubernetes OpenAPI schema types and a small `SchemaSource` interface. `pkg/xrdadapter` is the only package that knows XRDs exist, which is what would let a future `CRDConversionConfig` (for plain native CRDs) reuse almost all of this.

## Repository layout

```
api/v1alpha1/         XRDConversionConfig, ConversionWebhookServer CRD types
pkg/engine/            CRD-agnostic conversion engine: analyze, compile, convert
pkg/xrdadapter/        Crossplane XRD -> engine.SchemaSource adapter
pkg/crdadapter/        native CustomResourceDefinition -> engine.SchemaSource adapter
internal/assign/       shared "which ConversionWebhookServer serves this config" resolver
internal/controller/   the two reconcilers
internal/webhook/      this operator's own admission webhooks (validate XRDConversionConfig/ConversionWebhookServer)
internal/webhookserver/ the conversion webhook runtime (registry, HTTP handlers, metrics)
internal/cli/          convctl command implementations
cmd/manager/           operator binary
cmd/webhook-server/    conversion webhook runtime binary
cmd/convctl/        CLI binary
config/                kustomize manifests (kubebuilder dev-loop / CI)
charts/declarative-conversion-operator/  Helm chart (the supported install path)
```

## Quick start

```console
# Install (requires cert-manager and Crossplane already installed)
helm install declarative-conversion-operator charts/declarative-conversion-operator \
  --namespace declarative-conversion-system --create-namespace

# Wait for the default ConversionWebhookServer instance
kubectl wait --for=condition=Available conversionwebhookserver/default --timeout=120s

# Apply a config (see config/samples/ for an example)
kubectl apply -f config/samples/terasky_v1alpha1_xrdconversionconfig.yaml
kubectl get xrdconversionconfig xpostgresqlinstances-conversion -o yaml
```

## Conversion strategies

`fieldRename`, `scalarToObject` / `objectToScalar`, `singletonArrayToObject` / `objectToSingletonArray`, `fieldsToMap` / `mapToFields`, `toAnnotation` / `toLabel`, `enumRemap`, `defaultValue`, `constant`, `delete`, `jsonPatch` (escape hatch), `forEach` (per-array-element, one level of nesting), `typeCoerce`, `scalarToFields` / `fieldsToScalar`, `arrayToMapByKey` / `mapToArrayByKey`, `numericScale`, `listJoin` / `listSplit`. Every rule that the engine determines is lossy in any direction requires `acknowledgeLossy: true` plus an optional `reason` — this is enforced by both the admission webhook and the controller, and the default posture is fail-closed: any hub or spoke field left uncovered by a rule (and not structurally identical on both sides) is a validation error, not a silent pass.

A few of the newer strategies are worth calling out specifically:

- **`typeCoerce`** converts a scalar's JSON type (string/integer/number/boolean) at the same path on both sides — e.g. a field that was a string in one version and an integer in another. Always treated as lossless (canonically-formatted values round-trip exactly); a value that genuinely can't be parsed (a non-numeric string coerced to a number) is a runtime conversion error, not a lossiness concern.
- **`scalarToFields`** / **`fieldsToScalar`** decompose a single scalar into several fields (or the reverse) using a regexp with named capture groups (`pattern`) plus a Go `text/template` (`joinTemplate`) for the reverse direction — e.g. hub `spec.size: "3Gi"` split into spoke `spec.sizeValue: 3` / `spec.sizeUnit: "Gi"`. Like `jsonPatch`, the engine can't verify `pattern` and `joinTemplate` are true inverses of each other, so this is always lossy unless you set `losslessOverride: true` — real protection comes from running representative samples through `convctl test`, not from compile-time analysis.
- **`arrayToMapByKey`** / **`mapToArrayByKey`** convert a list of objects into a map keyed by one of their fields, and back — the standard "list-map versus map" API-evolution pattern. Array→map is lossless (a duplicate or missing key is a hard runtime error, never a silent drop); map→array is always treated as lossy, since the reconstructed array is emitted sorted by key rather than reproducing whatever order the original array had.
- **`numericScale`** rescales a numeric field by a fixed factor (`hubValue == spokeValue * factor`) — e.g. stored megabytes displayed as gigabytes. Whichever direction lands on an integer-typed field is treated as lossy, since the division/multiplication may not land on a whole number for every possible input.
- **`listJoin`** / **`listSplit`** convert an array of scalars into a single delimited string, and back. Always lossless; an element that happens to contain the separator as a substring will fail to round-trip cleanly, which is correctly surfaced by `convctl test` as a genuine data problem rather than an expected characteristic of the strategy.

`internal/cli/testdata/full` exercises all of these (and every other built-in strategy) end to end across a 3-version fixture — it's the best starting point for seeing exact YAML shapes in context.

### `status` fields

Rules apply to `status.*` paths exactly the same way they apply to `spec.*` — `pkg/engine` compiles and converts whatever top-level schema the XRD version declares (it never narrows to `.properties.spec`), so a `fieldRename` or any other strategy targeting `status.somePath` works identically to one targeting `spec.somePath`. This matters because a CRD conversion webhook receives the whole stored object, including `status`, regardless of whether the CRD uses the `status` subresource.

The same fail-closed coverage rule applies too: a `status` field that differs in shape between hub and spoke needs an explicit rule, or the config is rejected as invalid; a `status` field with identical shape on both sides is passed through automatically with no rule at all. `internal/cli/testdata/full` has a worked example — the hub's `status.phase` is renamed to `status.state` on the `v2` spoke via an ordinary `fieldRename` rule, while `status` is left untouched (identical shape, no rule) on the `v1` spoke.

## CLI

```console
convctl validate --config config.yaml [--xrd xrd.yaml]
convctl analyze   --xrd xrd.yaml --config config.yaml
convctl test       --xrd xrd.yaml --config config.yaml --samples ./samples/
```

`test` runs every sample object through every served-version conversion path (round-tripping through the hub), reporting timing, fields converted, rules exercised, and — for any detected loss — exactly which field diverged between which versions and whether it was acknowledged. Exit code `0` means every path passed or only had acknowledged loss; `1` means an unacknowledged loss or failure was found; `2` means a usage/tooling error.

### Pre-upgrade check: testing against everything that already exists

`--samples` is for hand-written fixtures. `--live` sources samples from a real cluster instead — every existing instance of the target XRD's generated composite resource type, fetched at its hub/storage version (so it works even before any conversion webhook is wired up):

```console
convctl test --xrd xrd.yaml --config new-config.yaml --live
convctl test --xrd xrd.yaml --config new-config.yaml --live --kubeconfig ~/.kube/other-config --context prod
```

This is the tool to run before applying a new or changed `XRDConversionConfig`: does it hold up against every object that already exists, not just fixtures? `--kubeconfig`/`--context` resolve exactly like `kubectl` does — an explicit `--kubeconfig` path falls back to `$KUBECONFIG`, then `~/.kube/config`; an explicit `--context` falls back to the kubeconfig's `current-context`. The invoking identity only needs `get`/`list` on the XRD's generated resource type — no write access, and nothing related to this operator's own CRDs or webhook server.

### Shell completion

`convctl completion [bash|zsh|fish|powershell]` (built into Cobra) prints a completion script for your shell — e.g. `source <(convctl completion bash)`, or see `convctl completion --help` for how to install it permanently.

## Development

```console
make generate manifests   # regenerate deepcopy code and CRD/RBAC YAML from kubebuilder markers
make test                  # go vet + unit tests (race-enabled)
make build                  # build all three binaries into bin/
make helm-lint helm-template
```

### Local dev environment

`make dev-up` stands up a full local environment — a [kind](https://kind.sigs.k8s.io/) cluster, cert-manager, Crossplane, and this operator's own Helm chart installed with images built from your local checkout — and leaves it running for interactive use. It's the same setup the e2e tests use (see below), minus the test assertions and the teardown. Safe to re-run after every code change: it rebuilds the images, reloads them into the cluster, and restarts the running pods so the new code actually takes effect. Requires `docker`, `kind`, `kubectl`, and `helm` on `PATH`; tear it down with `make dev-down`.

### End-to-end tests

Three scripts, all built on shared setup in `hack/e2e-common.sh`, prove the conversion webhook works against a real `kube-apiserver`, not just `pkg/engine` offline — each creates a [kind](https://kind.sigs.k8s.io/) cluster, builds this repo's `manager`/`webhook-server` images and loads them straight into the cluster (no registry push), and installs the operator via its own Helm chart:

- `make test-e2e` (`hack/e2e-test.sh`) — both features enabled (the common case): installs cert-manager and [Crossplane](https://crossplane.io) (v2 — this operator targets Crossplane's current `apiextensions.crossplane.io/v2` XRD API), applies a real `CompositeResourceDefinition` + `XRDConversionConfig` covering all 23 built-in strategies, and confirms composite resources created at every served version read back correctly converted at every other version.
- `make test-e2e-crd-only` (`hack/e2e-test-crd-only.sh`) — `features.crossplane.enabled=false`, Crossplane never installed at all: confirms the manager comes up healthy with no Crossplane CRDs on the cluster, that a `CRDConversionConfig` against a plain native CRD converts correctly, and that an `XRDConversionConfig` is rejected outright by the admission webhook.
- `make test-e2e-crossplane-only` (`hack/e2e-test-crossplane-only.sh`) — `features.nativeCRD.enabled=false`: confirms XRD/Crossplane conversion is unaffected by disabling native CRD support, and that a `CRDConversionConfig` is rejected outright.

Requires `docker`, `kind`, `kubectl`, and `helm` on `PATH`; all three run identically in CI (`.github/workflows/e2e.yml`, as a matrix) and locally. Set `KEEP_CLUSTER=1` to skip teardown for local debugging.

## License

Apache 2.0 — see [LICENSE](LICENSE).
