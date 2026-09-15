# Roadmap

This is a public summary of the project's phased intent — not a committed
timeline. Phases already shipped stay listed so the arc is visible; later phases
are invitations to [open an issue or PR](https://github.com/terasky-oss/declarative-conversion-operator/issues)
if one of them matters to you sooner.

## Shipped (phases 0–10)

| Phase | Epic | Intent |
|---|---|---|
| **0 — Correctness and API honesty** | [#5](https://github.com/terasky-oss/declarative-conversion-operator/issues/5) | Honest status, fail-closed defaults, and API shape the later phases build on. |
| **1 — Security hardening** | [#14](https://github.com/terasky-oss/declarative-conversion-operator/issues/14) | Restricted pod security, RBAC blast-radius docs, metrics trust boundary, NetworkPolicy options. |
| **2 — Resilience and operator correctness** | [#22](https://github.com/terasky-oss/declarative-conversion-operator/issues/22) | Drift policies, registry readiness, paced fan-out, admission/registry keying, delete race-window docs. |
| **3 — Observability pack** | [#29](https://github.com/terasky-oss/declarative-conversion-operator/issues/29) | Dashboards, expanded alerts, metric catalog (`target` label), manager metrics, optional OTLP tracing, HPA-on-QPS guidance. |
| **4 — CLI and authoring UX** | [#36](https://github.com/terasky-oss/declarative-conversion-operator/issues/36) | First-class `convctl diff` (config coverage/claim/lossy deltas — exposing analysis already used by `test`/`analyze`, not a from-scratch diff engine), `convert`, `suggest`, `--fail-on` matrix, parallel `--live`, SSA `patch-preview`. |
| **5 — Docs, examples, and community** | [#43](https://github.com/terasky-oss/declarative-conversion-operator/issues/43) | Curated `examples/`, kitchen-sink docs, operations runbooks, CONTRIBUTING/CODEOWNERS/CoC, strategy contribution guide, naming consistency, Artifact Hub metadata, this roadmap. |
| **6 — Helm and install UX** | [#52](https://github.com/terasky-oss/declarative-conversion-operator/issues/52) | Chart polish: values discoverability, CRD upgrade helper, optional cert-manager modes, `extraEnv`/`extraVolumes`, guided NOTES.txt. |
| **7 — Conversion strategy expansion** | [#59](https://github.com/terasky-oss/declarative-conversion-operator/issues/59) | Additional strategies driven by real migrations (non-string enum remap, quantity/duration helpers, optional CEL, …). The `Strategy` enum and discriminated `ConversionRule` union were built so each addition stays contained. |
| **8 — Crossplane integration depth** | [#68](https://github.com/terasky-oss/declarative-conversion-operator/issues/68) | Deeper Crossplane packaging/metadata pointers and a storage-version migration playbook. **Superseded on the v1 question:** Crossplane 1.x control planes are now an explicit non-goal, while the v1 compatibility layer *inside* Crossplane 2.x (`scope: LegacyCluster`, claims) is a first-class phase-11 target — see [Limitations](limitations.md). |
| **9 — Performance and scale** | [#74](https://github.com/terasky-oss/declarative-conversion-operator/issues/74) | Benchmark suite (compile vs schema size, Convert latency vs array length), registry Set scaling, synthetic large ConversionReview batches — numbers that backfill [Capacity planning](operations/capacity.md). |
| **10 — Multi-cluster / GitOps** | [#81](https://github.com/terasky-oss/declarative-conversion-operator/issues/81) | Documented CI patterns for `convctl test`/`diff` across many kubecontexts, GitOps examples (Flux/Argo). Cross-cluster failover of conversion state remains an explicit non-goal. |

## Proposed next phases

Phases 0–10 are complete. Each phase below has an epic with per-deliverable
sub-issues carrying a priority label, a size label, and a full PRD. The detail —
the review that produced the plan and the `file:line` findings behind each
entry — is in [Review and proposed next phases](proposals/next-phases.md).

| Phase | Epic | Intent |
|---|---|---|
| **11 — Crossplane integration depth** | [#113](https://github.com/terasky-oss/declarative-conversion-operator/issues/113) | Verify the conversion config actually propagated into Crossplane's generated CRD (a new `ConversionPropagated` condition); make `scope: LegacyCluster` and its claim CRD first-class (scope-aware regression tests for the exact injected-field set, `spec.claimNames` resolution in `test --live` and `migrate-storage`, a LegacyCluster e2e leg); Composition retarget without Kyverno (`convctl retarget`, `convctl crossplane status`); a mutating admission guard so an XRD shipped in a `Configuration` package stops losing its conversion webhook on every package resync (traced to a non-SSA `client.Update` in Crossplane's establisher); and Crossplane 2.x stated as the requirement it already is. |
| **12 — XRD/CRD API evolution lifecycle** | [#125](https://github.com/terasky-oss/declarative-conversion-operator/issues/125) | `convctl plan` as the lifecycle state machine, golden-corpus conversion testing (`--record` / `--golden`), output validation against the destination schema plus required-field analysis, property-based round-trip fuzzing, a `convctl compat` gate between two git refs, and version/deprecation inventory. |
| **13 — CI/CD: official GitHub Actions** | [#133](https://github.com/terasky-oss/declarative-conversion-operator/issues/133) | First-party composite Actions (`setup-convctl` with signature verification, `convctl-test`, `convctl-diff`, `convctl-fleet`) **with their own test workflow** — cross-runner matrix, a negative test proving verification fails on a tampered artifact, and assertions on annotations and job summaries, not just exit codes. Plus a published `convctl` image, CI-native output formats (GitHub annotations, SARIF, markdown), `convctl lint` over a whole repo, distribution via Homebrew / krew / deb / rpm, and a `--package` schema source so a Configuration's XRDs can be tested from a local `.xpkg`, a published image, or an installed `ConfigurationRevision`. |
| **14 — Production readiness** | [#145](https://github.com/terasky-oss/declarative-conversion-operator/issues/145) | Scoped informer caches (the manager currently caches every Secret in the cluster), HTTP timeouts and body limits on the conversion endpoint, rollout safety for the webhook-server, a curated `.golangci.yml`, supply-chain scanning, and a chart `values.schema.json`. |
| **15 — Performance and scale** | [#156](https://github.com/terasky-oss/declarative-conversion-operator/issues/156) | Automatic sharding across `ConversionWebhookServer` instances, a measured cold-start budget, per-target memory numbers, a nightly scale run at a raised envelope, and workqueue observability. |
| **16 — Engine and strategy expansion** | [#162](https://github.com/terasky-oss/declarative-conversion-operator/issues/162) | Required-field satisfaction analysis, `oneOf`/`anyOf` branch mapping, `$ref`/`allOf` flattening, and further strategies driven by real migrations. |

## Design seams worth knowing

- **Strategies stay additive.** A new strategy is a `*Params` type, an `Op`, a compile resolver, webhook validation, a CLI fixture, and a docs page — see [Adding a strategy](contributing/adding-a-strategy.md).
- **Observability is chart-optional.** ServiceMonitor / PrometheusRule / Grafana dashboards ship with the chart and stay off unless enabled.
- **Crossplane 2.x is the target; 1.x control planes are a non-goal.** The v1 compatibility layer inside 2.x — `scope: LegacyCluster`, claims, connection secrets — is in scope and is what phase 11 covers.
- **No cross-cluster coordination.** Every operator and webhook-server replica assumes a single cluster ([Architecture: One cluster, one install](architecture.md#one-cluster-one-install), [Limitations](limitations.md)).

---

Have a use case that doesn't fit any of the above? [Open an issue](https://github.com/terasky-oss/declarative-conversion-operator/issues) — real-world XRD/CRD migration patterns are what shape which phase moves next.
