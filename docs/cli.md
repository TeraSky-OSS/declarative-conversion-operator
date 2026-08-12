# CLI Reference: `convctl`

`convctl` runs the exact same `pkg/engine` code the operator and webhook server use, entirely offline against local YAML files — so you can validate and test a conversion mapping before it ever touches a cluster. Every command works identically against an `XRDConversionConfig` (pass `--xrd`) or a `CRDConversionConfig` (pass `--crd`) — which one applies is determined by the config file's own `kind`, not by which flag you happen to type, so passing the wrong one is a clear error rather than a silent mismatch.

```console
convctl validate --config config.yaml [--xrd xrd.yaml | --crd crd.yaml] [-o table|json]
convctl analyze   --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json]
convctl test      --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) (--samples ./samples/ | --live) [flags]
convctl diff      --config a.yaml --config b.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o json|table]
convctl diff      --config config.yaml --live [-o json|table]
convctl convert   --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) --sample obj.yaml --to v2 [-o yaml|json]
```

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

Each sample's asserted starting version is inferred from its own `apiVersion` — no separate index file needed.

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
