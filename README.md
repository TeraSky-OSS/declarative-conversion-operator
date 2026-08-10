# xrd-conversion-operator

A Kubernetes operator that lets admins declare field-level conversions between [Crossplane](https://crossplane.io) XRD (`CompositeResourceDefinition`) versions using built-in strategies — no hand-written conversion webhook required.

Crossplane XRDs support multiple version schemas the same way native CRDs do, but wiring up a real Kubernetes conversion webhook to convert between them today means writing and deploying custom Go code. This operator replaces that with a declarative `XRDConversionConfig` custom resource: pick a hub version, describe how each spoke version's fields map to it using named strategies (`fieldRename`, `scalarToObject`, `toAnnotation`, `enumRemap`, …), and the operator validates the mapping, compiles it, and — only once everything is verified healthy — patches the target XRD to route conversion requests to a shared, horizontally-scalable webhook server.

## Why

- **Declarative, not hand-written.** No Go code, no bespoke webhook deployment per XRD.
- **Conservative by default.** Any conversion the engine can't prove is lossless is rejected unless explicitly acknowledged (`acknowledgeLossy: true`).
- **Safe by construction.** The operator never patches a live XRD until the config is validated, the XRD is healthy, and the assigned webhook server is confirmed ready — and never reverts a working conversion setup on deletion if doing so would strand clients on a non-storage version.
- **Fast where it matters.** The webhook server precompiles every rule into a resolved-path execution plan and serves ConversionReview requests from an in-memory, per-replica registry — no network calls, no re-parsing, in the API admission critical path.
- **Testable offline.** The `xrdconvctl` CLI runs the exact same conversion engine against local YAML files and a directory of samples, before anything touches a cluster.

## Architecture

Two CRDs, API group `terasky.com/v1alpha1`:

- **`XRDConversionConfig`** (cluster-scoped) — one per target XRD. Declares a hub version and, per spoke version, a list of declarative conversion rules.
- **`ConversionWebhookServer`** (cluster-scoped) — a deployable, independently scalable instance of the shared conversion webhook runtime (its own Deployment, cert-manager `Certificate`, `Service`, `HorizontalPodAutoscaler`, `PodDisruptionBudget`). The Helm chart creates one by default, marked `default: true`; create more for scale-out or tenant isolation.

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
internal/assign/       shared "which ConversionWebhookServer serves this config" resolver
internal/controller/   the two reconcilers
internal/webhook/      this operator's own admission webhooks (validate XRDConversionConfig/ConversionWebhookServer)
internal/webhookserver/ the conversion webhook runtime (registry, HTTP handlers, metrics)
internal/cli/          xrdconvctl command implementations
cmd/manager/           operator binary
cmd/webhook-server/    conversion webhook runtime binary
cmd/xrdconvctl/        CLI binary
config/                kustomize manifests (kubebuilder dev-loop / CI)
charts/xrd-conversion-operator/  Helm chart (the supported install path)
```

## Quick start

```console
# Install (requires cert-manager and Crossplane already installed)
helm install xrd-conversion-operator charts/xrd-conversion-operator \
  --namespace xrd-conversion-system --create-namespace

# Wait for the default ConversionWebhookServer instance
kubectl wait --for=condition=Available conversionwebhookserver/default --timeout=120s

# Apply a config (see config/samples/ for an example)
kubectl apply -f config/samples/terasky_v1alpha1_xrdconversionconfig.yaml
kubectl get xrdconversionconfig xpostgresqlinstances-conversion -o yaml
```

## Conversion strategies

`fieldRename`, `scalarToObject` / `objectToScalar`, `singletonArrayToObject` / `objectToSingletonArray`, `fieldsToMap` / `mapToFields`, `toAnnotation` / `toLabel`, `enumRemap`, `defaultValue`, `constant`, `delete`, `jsonPatch` (escape hatch), `forEach` (per-array-element, one level of nesting). Every rule that the engine determines is lossy in any direction requires `acknowledgeLossy: true` plus an optional `reason` — this is enforced by both the admission webhook and the controller, and the default posture is fail-closed: any hub or spoke field left uncovered by a rule (and not structurally identical on both sides) is a validation error, not a silent pass.

## CLI

```console
xrdconvctl validate --config config.yaml [--xrd xrd.yaml]
xrdconvctl analyze   --xrd xrd.yaml --config config.yaml
xrdconvctl test       --xrd xrd.yaml --config config.yaml --samples ./samples/
```

`test` runs every sample object through every served-version conversion path (round-tripping through the hub), reporting timing, fields converted, rules exercised, and — for any detected loss — exactly which field diverged between which versions and whether it was acknowledged. Exit code `0` means every path passed or only had acknowledged loss; `1` means an unacknowledged loss or failure was found; `2` means a usage/tooling error.

## Development

```console
make generate manifests   # regenerate deepcopy code and CRD/RBAC YAML from kubebuilder markers
make test                  # go vet + unit tests (race-enabled)
make build                  # build all three binaries into bin/
make helm-lint helm-template
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
