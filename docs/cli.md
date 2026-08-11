# CLI Reference: `convctl`

`convctl` runs the exact same `pkg/engine` code the operator and webhook server use, entirely offline against local YAML files — so you can validate and test a conversion mapping before it ever touches a cluster. Every command works identically against an `XRDConversionConfig` (pass `--xrd`) or a `CRDConversionConfig` (pass `--crd`) — which one applies is determined by the config file's own `kind`, not by which flag you happen to type, so passing the wrong one is a clear error rather than a silent mismatch.

```console
convctl validate --config config.yaml [--xrd xrd.yaml | --crd crd.yaml] [-o table|json]
convctl analyze   --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json]
convctl test      --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) (--samples ./samples/ | --live) [flags]
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
