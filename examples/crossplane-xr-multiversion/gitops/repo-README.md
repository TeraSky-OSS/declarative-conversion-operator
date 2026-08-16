# XWidget lifecycle demo

This repository is the GitOps desired state for the
[declarative-conversion-operator](https://github.com/terasky-oss/declarative-conversion-operator)
multi-version Crossplane XR walkthrough.

| Path | Who | What |
|---|---|---|
| `platform/` | Platform | XRD, Compositions, Kyverno policies, functions |
| `conversion/` | Platform | `XRDConversionConfig` (same PR as the XRD; Flux applies this *after* the XRD is Ready) |
| `apps/` | App team | XWidgets. No `compositionRef`. |
| `.github/workflows/convctl.yaml` | CI | `convctl validate` / `test --samples` / `test --live` on the in-cluster runner; posts the output as a PR comment |

PRs must go green on the self-hosted `xwidget-demo` runner before merge.
Flux or Argo then reconciles `main` onto the demo cluster.

`convctl migrate-storage` is cluster housekeeping, not desired-state YAML —
it is run locally from the walkthrough, not from this repo.
