# CLI Reference: `convctl`

`convctl` runs the exact same `pkg/engine` code the operator and webhook server use, entirely offline against local YAML files — so you can validate and test a conversion mapping before it ever touches a cluster. Every command works identically against an `XRDConversionConfig` (pass `--xrd`) or a `CRDConversionConfig` (pass `--crd`) — which one applies is determined by the config file's own `kind`, not by which flag you happen to type, so passing the wrong one is a clear error rather than a silent mismatch.

```console
convctl validate      --config config.yaml [--xrd xrd.yaml | --crd crd.yaml] [-o table|json]
convctl analyze       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json]
convctl test          --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) (--samples ./samples/ | --live) [flags]
convctl diff          --config a.yaml --config b.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o json|table]
convctl diff          --config config.yaml --live [-o json|table]
convctl convert       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) --sample obj.yaml --to v2 [-o yaml|json]
convctl suggest       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o yaml|json]
convctl patch-preview --config config.yaml --service-name NAME --service-namespace NS --ca-bundle B64 [flags]
```

Roughly in the order you reach for them while authoring a mapping: `suggest` drafts rules for fields nothing covers yet, `validate` and `analyze` check the config statically, `convert` shows what a single object turns into, `test` grades fixtures or every live object, `diff` reports what a config edit changed, and `patch-preview` shows the exact patch the operator will apply once you commit.

## `convctl validate`

Runs the same static checks the admission webhook performs, offline.

```console
convctl validate --config xrdconversionconfig.yaml
convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl validate --config crdconversionconfig.yaml --crd crd.yaml
```

| Flag | Description |
|---|---|
| `-c, --config` | Path to an `XRDConversionConfig` or `CRDConversionConfig` YAML file. **Required.** |
| `-x, --xrd` | Path to an XRD YAML file. Optional — supplying it enables full schema validation (compiling every rule against the real hub/spoke schemas); without it, only structural checks on the config itself run. Mutually exclusive with `--crd`. |
| `--crd` | Path to a CRD YAML file, for a `CRDConversionConfig`. Same optionality as `--xrd`. |
| `-o, --output` | `table` (default) or `json`. |

## `convctl analyze`

Schema-only lossy/coverage analysis — no sample objects needed. Answers "if I apply this config, will it validate, and what's lossy?" without needing any example data.

```console
convctl analyze --xrd xrd.yaml --config xrdconversionconfig.yaml
convctl analyze --crd crd.yaml --config crdconversionconfig.yaml
```

| Flag | Description |
|---|---|
| `-x, --xrd` | Path to an XRD YAML file. Required for an `XRDConversionConfig`; mutually exclusive with `--crd`. |
| `--crd` | Path to a CRD YAML file. Required for a `CRDConversionConfig`. |
| `-c, --config` | Path to an `XRDConversionConfig` or `CRDConversionConfig` YAML file. **Required.** |
| `-o, --output` | `table` (default) or `json`. |

## `convctl test`

Runs every sample object through every served-version conversion path (round-tripping through the hub) and reports timing, fields converted, rules exercised, and — for any detected loss — exactly which field diverged between which versions and whether it was acknowledged.

```console
convctl test --xrd xrd.yaml --config xrdconversionconfig.yaml --samples ./samples/
convctl test --crd crd.yaml --config crdconversionconfig.yaml --samples ./samples/
```

| Flag | Description |
|---|---|
| `-x, --xrd` | Path to an XRD YAML file. Required for an `XRDConversionConfig`; mutually exclusive with `--crd`. |
| `--crd` | Path to a CRD YAML file. Required for a `CRDConversionConfig`. |
| `-c, --config` | Path to an `XRDConversionConfig` or `CRDConversionConfig` YAML file. **Required.** |
| `-s, --samples` | Path to a directory of sample objects — one file per sample (or multi-doc YAML). Mutually exclusive with `--live`; exactly one of the two is required. |
| `--live` | Fetch samples from a live cluster instead — see [Pre-upgrade checks](#pre-upgrade-checks-testing-against-everything-that-already-exists) below. |
| `--kubeconfig` | Path to a kubeconfig file. Only used with `--live`. Falls back to `$KUBECONFIG`, then `~/.kube/config`, exactly like `kubectl`. |
| `--context` | Kubeconfig context to use. Only used with `--live`. Falls back to the kubeconfig's `current-context`. |
| `-o, --output` | `table` (default), `json`, or `junit` — see [Output formats](#output-formats) below. |
| `--output-file` | Write the full report to this file instead of stdout. A short pass/loss/fail/error summary is still printed to stdout either way, so a CI log isn't empty on success. |
| `--strict` | Escalate warnings (e.g. a rule that's never exercised by any sample) to failures. |
| `--fail-on` | Exit-code threshold: `none`, `warn`, or `loss` (default). |
| `--version-pair` | Restrict testing to specific version(s). Repeatable. |
| `--skip-identity` | Skip trivial same-version passthrough checks. |
| `--concurrency` | How many samples to test in parallel. Defaults to one worker per available CPU. |
| `--quiet` | Suppress the progress line written to stderr. |

Each sample's asserted starting version is inferred from its own `apiVersion` — no separate index file needed.

### Parallelism and progress

Samples are tested in parallel, one worker per available CPU by default. This matters most for `--live`, where the sample set is every object of the target type in the cluster rather than a handful of fixtures. Set `--concurrency N` to pin the worker count (`--concurrency 1` to go fully sequential).

Parallelism never changes the result. Every sample is independent, and results are collected by sample index rather than by completion order, so the report — including the order samples appear in — is byte-for-byte what a single worker would have produced. The paths *within* one sample stay sequential.

While more than one sample is in flight, a `tested N/M samples` progress line is rewritten on stderr, leaving stdout clean for `--output json`/`junit` pipes. Pass `--quiet` to suppress it.

### Output formats

`--output table` (default) is meant for a terminal. `--output json` mirrors the same report structure for scripting. `--output junit` renders the report as JUnit XML — one `<testcase>` per sample/path pair tested, with unacknowledged loss and conversion errors mapped to `<failure>`/`<error>` respectively (an acknowledged loss stays a passing testcase, with detail attached as `<system-out>`) — for CI systems with JUnit test-result reporting (GitHub Actions' test-reporting integrations, GitLab, Jenkins). Combine with `--output-file` to write the report where your CI step expects it:

```console
convctl test --xrd xrd.yaml --config xrdconversionconfig.yaml --samples ./samples/ \
  --output junit --output-file report.junit.xml
```

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Every conversion path passed, or any loss found was already acknowledged (`acknowledgeLossy: true`). |
| `1` | An unacknowledged loss or failure was found, at or above the `--fail-on` threshold. |
| `2` | A tool/usage error (bad flags, file not found, etc.) — deliberately distinct from a test-result failure, so CI can tell "the tool broke" from "the config is bad." |

`--fail-on` takes exactly `none`, `warn`, or `loss`; anything else is a usage error (`2`) rather than a silently-ignored typo that would let a broken gate report success forever.

Here is every threshold against every outcome:

| Run outcome | `--fail-on none` | `--fail-on warn` | `--fail-on loss` (default) |
|---|---|---|---|
| Everything passed | `0` | `0` | `0` |
| Acknowledged loss only | `0` | `0` | `0` |
| Unacknowledged loss | `0` | `1` | `1` |
| Conversion error | `0` | `1` | `1` |
| A declared rule no sample exercised | `0` | `1` | `0` |

**Acknowledged loss alone never fails, at any threshold.** `acknowledgeLossy: true` is the config author stating on the record that a field is expected to be dropped or rounded; re-litigating that decision on every CI run would just train people to pass `--fail-on none`. What the default threshold catches is loss that *nobody* declared.

`--strict` escalates coverage gaps exactly the way `--fail-on warn` does — a declared rule that no sample exercised becomes a failure. So `--fail-on loss --strict` behaves identically to `--fail-on warn`, and `--strict` changes nothing when `--fail-on warn` is already set. `--fail-on none` overrides `--strict` entirely: it is the explicit "report, never gate" switch, and always exits `0`.

## `convctl diff`

Analyzes two conversion configs against the same schema and reports what changed between them — the review question "what does this config edit actually do?", answered in terms of coverage and lossiness rather than YAML lines.

```console
convctl diff --xrd xrd.yaml --config current.yaml --config proposed.yaml
convctl diff --crd crd.yaml --config current.yaml --config proposed.yaml -o table
convctl diff --config proposed.yaml --live
```

| Flag | Description |
|---|---|
| `-c, --config` | Path to a conversion config YAML file. **Required.** Pass it exactly twice to compare two files, or exactly once together with `--live`. Both files must be the same kind. |
| `-x, --xrd` | Path to an XRD YAML file. Required in two-file mode for `XRDConversionConfig`s; mutually exclusive with `--crd`. Ignored with `--live`, which reads the schema from the cluster. |
| `--crd` | Path to a CRD YAML file. Required in two-file mode for `CRDConversionConfig`s. |
| `--live` | Compare the single `--config` against the cluster: the live XRD/CRD supplies the schema, and the `XRDConversionConfig`/`CRDConversionConfig` of the same name supplies the other side. |
| `--kubeconfig` | Path to a kubeconfig file. Only used with `--live`. Resolves exactly like `kubectl`. |
| `--context` | Kubeconfig context to use. Only used with `--live`. |
| `-o, --output` | `json` (default) or `table`. |

The report is per-spoke, listing:

- hub and spoke fields that became uncovered, and ones that became covered
- rule claims added and removed, identified by strategy plus the hub/spoke paths they claim (so reordering rules is correctly *not* a difference)
- lossless flags that flipped, per direction
- error and warning messages that appeared or disappeared

Plus, at the top level, a hub-version change and any spoke version added or removed outright.

With `--live`, the cluster is always the *from* side and the local file is the *to* side, so the diff reads as "what applying this file would change". If the cluster has no config of that name yet, the from side becomes an empty rule set over the same spoke versions — every rule in your file shows up as an addition, which is a far more useful answer than refusing to run.

Exit codes are `0` when the two sides are equivalent, `1` when any delta is found, and `2` for usage or load errors — so `convctl diff` drops straight into a CI gate that fails a PR whose config change wasn't intended.

## `convctl convert`

Converts a single object and prints the result. Where `convctl test` round-trips fixtures and grades them, `convert` is the "just show me the output" tool — for eyeballing what a rule actually produces, or for piping a converted object straight into `kubectl apply`.

```console
convctl convert --xrd xrd.yaml --config xrdconversionconfig.yaml --sample widget-v1.yaml --to v3
convctl convert --crd crd.yaml --config crdconversionconfig.yaml --sample widget.yaml --to v2 -o json
```

| Flag | Description |
|---|---|
| `-c, --config` | Path to an `XRDConversionConfig` or `CRDConversionConfig` YAML file. **Required.** |
| `-x, --xrd` | Path to an XRD YAML file. Required for an `XRDConversionConfig`; mutually exclusive with `--crd`. |
| `--crd` | Path to a CRD YAML file. Required for a `CRDConversionConfig`. |
| `--sample` | Path to a single-document YAML file holding the object to convert. **Required.** A multi-document file is an error, not a silent "convert the first one". |
| `--to` | Version to convert into. **Required.** |
| `--from` | Source version. Defaults to the version in the sample's own `apiVersion`. |
| `-o, --output` | `yaml` (default) or `json`. |

The output object's `apiVersion` is rewritten to `<group>/<to>`, with the group taken from the XRD's or CRD's `spec.group` — the engine itself only ever knows version *names*, so the schema is the only thing that can supply the group. Spoke-to-spoke conversions route through the hub exactly as they do in production, and a config that doesn't validate against the schema is refused before any conversion runs.

```console
$ convctl convert --xrd xrd.yaml --config config.yaml --sample sample1-v1.yaml --to v2
apiVersion: example.org/v2
kind: Foo
metadata:
  name: sample1
spec:
  storageGB: "100"
```

## `convctl suggest`

Proposes rule stubs for the fields a config leaves uncovered — the tedious half of authoring a mapping between two versions. Point it at a config with no rules at all and it bootstraps a first draft; point it at a half-finished one and it fills in what's still missing.

```console
convctl suggest --xrd xrd.yaml --config xrdconversionconfig.yaml
convctl suggest --crd crd.yaml --config crdconversionconfig.yaml -o json
```

| Flag | Description |
|---|---|
| `-c, --config` | Path to an `XRDConversionConfig` or `CRDConversionConfig` YAML file. **Required.** |
| `-x, --xrd` | Path to an XRD YAML file. Required for an `XRDConversionConfig`; mutually exclusive with `--crd`. |
| `--crd` | Path to a CRD YAML file. Required for a `CRDConversionConfig`. |
| `-o, --output` | `yaml` (default) or `json`. |

Two kinds of suggestion are made, per spoke:

- **`FieldRename`**, when an uncovered hub field and an uncovered spoke field sit under the same parent path, declare the same type, and have similar enough names after normalizing away casing and punctuation (`storageGB` ↔ `storage_size`).
- **`TypeCoerce`**, when a field keeps its exact path but changes scalar type between the two versions.

Nothing else is guessed at. A `ScalarToFields` split or an `ArrayToMapByKey` restructure is a design decision, not a pattern to infer from two schemas.

```console
$ convctl suggest --xrd xrd.yaml --config config-with-no-rules.yaml
# suggested rules — review every one before merging into spec.spokes
spokes:
- rules:
  - strategy: TypeCoerce
    typeCoerce:
      path: spec.priority
  - fieldRename:
      hubPath: spec.storageGB
      spokePath: spec.storageSize
    strategy: FieldRename
  version: v2
```

The output is shaped exactly like a config's `spec.spokes` stanza, so accepted suggestions paste straight in.

**These are heuristics, not conclusions.** Nothing can prove two differently-named fields were meant to be the same one, and name similarity will occasionally pair the wrong two. Read every suggestion, delete the wrong ones, and let `convctl validate` and `convctl test` grade what's left — a suggestion that survives both is a rule you can trust, and one that doesn't cost you a deleted line.

## `convctl patch-preview`

Prints the exact server-side-apply object the operator would send to the target XRD or CRD to point its `spec.conversion` at a webhook server. Useful for reviewing a change before granting the operator write access to a production XRD, and for understanding what "the operator patches your XRD" actually means in concrete YAML.

```console
convctl patch-preview --config xrdconversionconfig.yaml --xrd xrd.yaml \
  --service-name prod-webhook-server --service-namespace conversion-system \
  --ca-bundle "$(kubectl get secret prod-webhook-server-tls -n conversion-system -o jsonpath='{.data.ca\.crt}')"
```

| Flag | Description |
|---|---|
| `-c, --config` | Path to an `XRDConversionConfig` or `CRDConversionConfig` YAML file. **Required.** Supplies the target name, the config name for the `managed-by` annotation, and `conversionReviewVersions`. |
| `--service-name` | Name of the webhook server `Service`. **Required.** |
| `--service-namespace` | Namespace of the webhook server `Service`. **Required.** |
| `--ca-bundle` | CA bundle, either base64-encoded (as it appears in a CRD's YAML) or raw PEM, which is detected and encoded for you. **Required.** |
| `--path` | Webhook path. Defaults to `/convert/<target name>`, which is what the operator derives. |
| `--port` | Webhook `Service` port. Defaults to `443`. |
| `--plan-hash` | Value for the `conversion.terasky.com/plan-hash` annotation. Defaults to empty, which is what it is before the config's first successful validation. |
| `-x, --xrd` / `--crd` | Optional. Supplying the schema validates the config against it first, so a config the operator would refuse to apply doesn't get a preview of being applied. |

```console
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  annotations:
    conversion.terasky.com/managed-by: xfoos-conversion
    conversion.terasky.com/plan-hash: sha256:abc
  name: xfoos.example.org
spec:
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        caBundle: UEVN
        service:
          name: prod-webhook-server
          namespace: conversion-system
          path: /convert/xfoos.example.org
          port: 443
      conversionReviewVersions:
      - v1
```

The patch is built by `internal/conversionpatch`, the same package the controllers call — so what you see here cannot drift from what the operator applies. That is also why the service coordinates and CA bundle are flags rather than cluster lookups: `patch-preview` never constructs a Kubernetes client at all, so it cannot touch a cluster even by accident.

Note that this is the *patch*, not the resulting object. It is applied with `ForceOwnership` under the `declarative-conversion-operator` field manager, so it claims exactly the fields shown and leaves every other field on the XRD or CRD to whoever owns it.

## Pre-upgrade checks: testing against everything that already exists

`--samples` is for hand-written fixtures. `--live` sources samples from a real cluster instead — every existing instance of the target XRD's generated composite resource type (or, for a `CRDConversionConfig`, the native CRD's own resource type), fetched at its hub/storage version (so it works even *before* any conversion webhook is wired up, since the storage version is always readable):

```console
convctl test --xrd xrd.yaml --config new-config.yaml --live
convctl test --crd crd.yaml --config new-crd-config.yaml --live \
  --kubeconfig ~/.kube/other-config --context prod
```

This is the tool to run before applying a new or changed `XRDConversionConfig`/`CRDConversionConfig`: does it hold up against every object that already exists in the cluster, not just your fixtures? `--kubeconfig`/`--context` resolve exactly like `kubectl` does. The invoking identity only needs `get`/`list` on the target resource type — no write access, and nothing related to this operator's own CRDs or webhook server.

## Shell completion

`convctl completion [bash|zsh|fish|powershell]` (built into Cobra) prints a completion script for your shell:

```console
source <(convctl completion bash)
```

See `convctl completion --help` for how to install it permanently for your shell.
