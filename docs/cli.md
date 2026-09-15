# CLI Reference: `convctl`

`convctl` runs the exact same `pkg/engine` code the operator and webhook server use, entirely offline against local YAML files — so you can validate and test a conversion mapping before it ever touches a cluster. Most commands work identically against an `XRDConversionConfig` (pass `--xrd`) or a `CRDConversionConfig` (pass `--crd`) — which one applies is determined by the config file's own `kind`, not by which flag you happen to type, so passing the wrong one is a clear error rather than a silent mismatch. `migrate-storage` is the exception: it is a live, mutating housekeeping command that takes cluster resource names (not files) and does not need a conversion config.

```console
convctl lint          [path...] [--schema-dir dir] [--exclude glob] [-o table|json|github|sarif|markdown]
convctl validate      --config config.yaml [--xrd xrd.yaml | --crd crd.yaml] [-o table|json|github|sarif|markdown]
convctl analyze       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json|github|sarif|markdown]
convctl test          --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) (--samples ./samples/ | --live) [-o table|json|junit|github|sarif|markdown] [flags]
convctl plan          --to v2 (--xrd xrd.yaml | --crd crd.yaml) [--config config.yaml] [-o table|json]
convctl versions      --xrd xrd.yaml [--config config.yaml] [--check-unserve v1] [-o table|json]
convctl compat        --base REV --head REV --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json|markdown]
convctl diff          --config a.yaml --config b.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json|markdown]
convctl diff          --config config.yaml --live [-o json|table]
convctl convert       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) --sample obj.yaml --to v2 [-o yaml|json]
convctl suggest       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o yaml|json]
convctl rehub         --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) --to v3 [-o yaml|json]
convctl generate kyverno --xrd xrd.yaml --to v2 [--from v1] [-o yaml|json]
convctl patch-preview --config config.yaml --service-name NAME --service-namespace NS --ca-bundle B64 [flags]
convctl migrate-storage (--xrd NAME | --crd NAME) [flags]
convctl retarget      --xrd NAME --to v2 [--dry-run] [--canary N|N%] [flags]
convctl crossplane status <xrd-name> [-o table|json]
```

`plan` is the one to start from if you are mid-migration and unsure what comes next: it prints the ordered, gated path from the target's current state to the version you name, and marks the one step that is safe to do now.

Roughly in the order you reach for them while authoring a mapping: `suggest` drafts rules for fields nothing covers yet, `validate` and `analyze` check the config statically, `convert` shows what a single object turns into, `test` grades fixtures or every live object, `diff` reports what a config edit changed, and `patch-preview` shows the exact patch the operator will apply once you commit. After a hub/storage-version promotion, `migrate-storage` rewrites live objects (critical for native CRDs; on XRDs the `compositionRef` retarget usually already did, and the remaining job is pruning `storedVersions`). For a GitOps hub flip, `generate kyverno` drafts MutatingPolicies that retarget existing XRs without a per-object name patch; on a cluster without Kyverno, `retarget` does the same job directly. `crossplane status` answers "where is my migration right now?" without assembling it from half a dozen `kubectl` invocations. Around all of it, `plan` sequences the migration, `versions` answers whether an old version can be retired yet, and `compat` gates config edits in review.

## Running it in a container

```console
docker run --rm -v "$PWD:/work" -w /work \
  ghcr.io/terasky-oss/declarative-conversion-convctl:v0.5.0 \
  lint ./platform/
```

Published on every release for `linux/amd64` and `linux/arm64`, signed and
attested like the operator images. It is distroless and has no shell, so run
one `convctl` invocation per step rather than chaining — see
[Installation: the `convctl` container image](installation.md#the-convctl-container-image).

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

Runs every sample object through every conversion path the config declares — hub plus each compiled spoke, among served versions (round-tripping through the hub) — and reports timing, fields converted, rules exercised, and — for any detected loss — exactly which field diverged between which versions and whether it was acknowledged.

A served version that is not a spoke is not a conversion path. Drop the spoke from the config before setting `served: false`; `convctl test` still runs in that window. A sample whose own `apiVersion` is that dropped version is an **ERROR** — move the object in git to a remaining spoke or the hub first.

`--samples` may be a GitOps `apps/` tree. Documents that are not the XRD/CRD's group and kind (for example `kustomization.yaml`) are ignored.

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
| `--verify-propagation` | With `--live` on an XRD, also read every CRD Crossplane generates from it and check it carries the conversion webhook the XRD points at. Samples passing through the engine says the rules are right; this says the cluster will actually use them. Until Crossplane re-renders the generated CRD, reads at a non-storage version return stored objects relabelled but **unconverted**, with HTTP 200 and no error. |
| `--kubeconfig` | Path to a kubeconfig file. Only used with `--live`. Falls back to `$KUBECONFIG`, then `~/.kube/config`, exactly like `kubectl`. Mutually exclusive with `--kubeconfig-dir`. |
| `--context` | Kubeconfig context to use. Only used with `--live`. Falls back to the kubeconfig's `current-context`. Mutually exclusive with `--contexts`. |
| `--contexts` | Repeatable / comma-separated kubeconfig context names. Runs `--live` once per context and aggregates the report. A single name keeps the one-cluster report shape. |
| `--kubeconfig-dir` | Directory of kubeconfig files. `--live` runs against each file (that file's `current-context`, or each `--contexts` name). README / hidden files are skipped. |
| `-o, --output` | `table` (default), `json`, or `junit` — see [Output formats](#output-formats) below. |
| `--output-file` | Write the full report to this file instead of stdout. A short pass/loss/fail/error summary is still printed to stdout either way, so a CI log isn't empty on success. |
| `--strict` | Escalate warnings (e.g. a rule that's never exercised by any sample) to failures. |
| `--fail-on` | Exit-code threshold: `none`, `warn`, or `loss` (default). |
| `--version-pair` | Restrict testing to specific version(s). Repeatable. |
| `--skip-identity` | Skip trivial same-version passthrough checks. |
| `--concurrency` | How many samples to test in parallel. Defaults to one worker per available CPU. |
| `--quiet` | Suppress the progress line written to stderr. |

Each sample's asserted starting version is inferred from its own `apiVersion` — no separate index file needed.

### Property-based testing (`--fuzz`)

Fixtures test the cases the config author thought of. They reliably miss the
empty array, the absent optional object, the `maxLength` boundary, the enum
value nobody uses, and the single-element map — which is exactly where
conversion rules break.

`--fuzz N` generates N schema-valid objects from the hub version's own schema
and runs them through every conversion path:

```console
$ convctl test --xrd xrd.yaml --config config.yaml --fuzz 50 --seed 42
FUZZ: 50 generated object(s), seed 42 — reproduce with --fuzz 50 --seed 42
...
fuzz-1  (conversion)  v3 → v2  error  jsonPatch: apply: move operation does not
                                      apply: doc is missing from path:
                                      /spec/legacyFlag: missing value
```

That example is not hypothetical: it is what `--fuzz` reports against this
repository's own full-coverage fixture, which exercises all 29 strategies.
Four classes come out of it. Three are the same shape — a rule that assumes
an optional field is present — and the fourth is a schema that admits a
lexical form its rule cannot represent:

| What fails | Why |
|---|---|
| `jsonPatch: move ... doc is missing from path: /spec/legacyFlag` | `move` on a field the schema does not require |
| `cel: no such key: spec` | an expression assuming `spec` exists, on an object where every property is optional |
| `arrayToMapByKey: element 0 ... is missing key field "name"` | the key field is not `required` in the item schema |
| `duration: "27us" is not a whole number of seconds` | the schema types the field as `string` and permits durations the rule cannot represent |

Each one is a conversion that would fail on a real object the apiserver would
have accepted. Fixtures had covered every strategy in that config and found
none of them.

Generation is **biased toward the boundaries** rather than toward typical
values: empty and single-element arrays, arrays at `maxItems`, strings at
`minLength` and `maxLength`, numbers at `minimum` and `maximum`, the first
and last enum members, and absent optionals.

`--seed` makes a run reproducible, and the seed is printed on every run so a
CI failure is replayable locally. **Use a fixed seed in a gating job** so the
job is deterministic; leave it off for exploratory runs.

`--fuzz` composes with `--samples` (generated objects alongside the ones you
wrote) and with `--validate-output`, which together are the strongest
offline check available: generated inputs, and the destination schema applied
to the result.

#### Promoting a discovered failure

`--record-failures <dir>` writes every object that failed conversion as an
ordinary sample file. Move it into your fixtures and it is tested on every
run by everyone, instead of depending on somebody re-rolling the same seed.
That promotion path is what makes fuzzing pay off over time.

#### What the generator will not invent

A schema can say `type: string` while a rule expects a Kubernetes quantity, a
Go duration, or a number. A random string there is schema-valid and certain
to fail conversion — which reads as a conversion bug and is not one. So the
generator reads the compiled rules and produces the right lexical shape for
`quantity`, `duration`, `numericScale` and `typeCoerce` paths.

For paths whose form it cannot construct — a `pattern` it cannot invert, a
`cel` expression, a `jsonPatch`, a split/join template — it leaves the field
**absent** rather than filling it with something guaranteed to be rejected.
Absence is a legitimate input and a real boundary; a random string is
neither.

That works only while the field is optional. A **required** field it cannot
construct would make every generated object violate the schema it was
generated from, so `--fuzz` stops and says so, naming the field and whether
the obstacle is the schema's pattern or a rule's lexical shape:

```console
cannot generate objects for v3: required field "spec.serial" cannot be filled — the schema
constrains it with pattern "^SN-[0-9]{6}-[A-Z]{3}$", which the generator cannot invert, and it
has neither a default nor an enum to fall back on. Give it one in the schema, or test that path
with --samples instead of --fuzz
```

Generated arrays and strings are also capped at 256 elements or bytes
regardless of what `minItems` / `maxLength` say, and a *minimum* above that
cap makes the field unconstructible. A schema asking for a million-element
array is not a fuzz case worth allocating.

Every generated object is validated against the schema it was generated from
before any conversion runs. A generator bug is reported as a generator bug,
with the seed, rather than being allowed to masquerade as a conversion
failure.

Runs are capped at 10,000 objects: a fuzz count large enough to hang CI is a
footgun, not a feature.

### Golden-corpus testing (`--record` / `--golden`)

When a conversion config changes, a reviewer sees a YAML diff of the rules
and a green check. Neither shows **what the change does to real objects**,
which is the only thing that matters.

`--record` writes the conversion result for every sample on every path into a
corpus you commit alongside the config:

```console
$ convctl test --xrd xrd.yaml --config config.yaml --samples ./samples --record ./golden
GOLDEN CORPUS: recorded 6 conversion(s) into ./golden

$ find golden -type f
golden/manifest.yaml
golden/hub-v3/v3-to-v1.yaml
golden/hub-v3/v3-to-v2.yaml
golden/spoke-v1/v1-to-v2.yaml
...
```

One object per file, named `<sample>/<from>-to-<to>.yaml`, so a change shows
up in exactly the files it affects. Output is byte-stable: keys are sorted,
numbers are formatted deterministically, and nothing carries a timestamp — a
re-record that reshuffled keys would bury the real change in noise.

`--golden` replays it and fails on any difference:

```console
$ convctl test --xrd xrd.yaml --config config.yaml --samples ./samples --golden ./golden
GOLDEN CORPUS: ./golden — 1 difference(s)
  KIND     FILE                  DETAIL
  changed  hub-v3/v3-to-v1.yaml  spec.cpuLimit
```

**The PR diff then *is* the behavioural change.** A rule edit that alters
output shows the affected objects and fields, in review, before merge.

Three failures, each with its own remedy, so they are never collapsed into
"the corpus does not match":

| Kind | Means | Remedy |
|---|---|---|
| `changed` | a converted value differs; the fields are named | confirm it is intended, then re-record |
| `missing` | a conversion produced output with no golden | re-record, or add the sample to the corpus |
| `orphaned` | a golden nothing produces any more | re-record if the path was dropped deliberately |

The corpus carries a `manifest.yaml` recording the plan hash, schema hash and
convctl version. A mismatch is a **loud warning, not a failure** — the corpus
may legitimately predate a config edit, and the field diffs are the real
evidence either way.

Drift fails the run regardless of `--fail-on`. That threshold grades
conversion quality; a corpus mismatch is a fact rather than a judgement, and
a gate `--fail-on none` could switch off would not be a gate.

`--record` and `--golden` are mutually exclusive.

#### Recording from live objects

`--live --record` builds the corpus from what is actually in the cluster —
the highest-value form, because every future config change is then graded
against real data with no cluster access and no secrets in CI.

> [!WARNING]
> A corpus recorded from live objects contains real field values, which may
> include sensitive data. Review it before committing.

### Validating the converted output (`--validate-output`)

`convctl test` round-trips every sample and diffs the result. That proves the
rules agree **with each other**. It says nothing about whether what they
produced is an object the apiserver will accept — and those are different
questions.

Without this flag, a conversion that drops a `required` field, produces a
value outside an `enum`, violates a `pattern`, or overflows a `maxLength` is
reported as **PASS**, and is then rejected in production with an error that
names the object rather than the rule that produced it.

```console
$ convctl test --xrd xrd.yaml --config config.yaml --samples ./samples
          v2→v1  PASS    4  16   v1:rule[1]:FieldRename,v1:rule[2]:FieldRename

$ convctl test --xrd xrd.yaml --config config.yaml --samples ./samples --validate-output
          v2→v1  ERROR   4  136  v1:rule[1]:FieldRename,v1:rule[2]:FieldRename

ISSUES (2)
SAMPLE    FIELD        FROM → TO  TYPE              DETAIL
hub.yaml  spec.region  v2 → v1    schema-violation  spec.region: Required value (required) [v1:rule[2]:FieldRename]
hub.yaml  spec.tier    v2 → v1    schema-violation  spec.tier: Unsupported value: "bronze": supported values: "gold", "silver" (enum) [v1:rule[1]:FieldRename]
```

Each violation carries the JSON path, the constraint that failed, and — when
exactly one rule claims that destination path — the rule that produced it.
Attribution is best-effort: a violation no single rule claims is still
reported, because one nobody can explain matters more, not less.

Validation uses the apiserver's own structural-schema validator
(`k8s.io/apiextensions-apiserver/pkg/apiserver/validation`), not a
reimplementation, so the verdict matches what the cluster will do rather than
approximating it.

**A violation is an error, not a loss.** It counts in `Summary.Errors` and
fails at the default `--fail-on loss` threshold — an object the apiserver
rejects is a failed conversion, not a lossy one.

**Crossplane's injected fields do not produce violations.** Crossplane merges
`spec.crossplane` (and, for `LegacyCluster`, `spec.claimRef`,
`spec.writeConnectionSecretToRef` and the rest) into the CRD it generates,
but those properties are absent from the XRD's authored schema — the only
schema this tool has. They are removed before validation, per the target's
resolved scope, so a real Crossplane object reports only violations its
author actually caused.

> [!NOTE]
> **Off by default in this release, on in a later one.** Turning it on now
> would make existing green pipelines red on upgrade without warning. New
> pipelines should set it; the default is planned to flip in a future
> release, and the change will be called out in the release notes.

### Bounded sampling on a large cluster

`--live` lists and tests every object of the target type. On a cluster with
tens of thousands of composites that is exactly where a pre-upgrade check is
most valuable and least able to run.

`--max-samples <n>` caps what gets tested, with `--sample-strategy`:

| Strategy | Behaviour | Cost |
|---|---|---|
| `first` (default) | stops listing at the cap | cheapest — the only one that can stop early |
| `random` | reservoir-samples while paginating, so the whole population is represented without ever being held | lists everything, holds `n` |
| `newest` | the `n` most recently created objects, where a schema change shows up first | lists everything, holds `n` |

`random` is reproducible: pass `--seed` and the same objects are chosen, so a
CI failure can be re-run rather than re-rolled.

**A sampled run says so, in every format.** The table prints it, the JSON
carries a `sampling` block, and the JUnit suite carries `sampled`,
`samplePopulation` and `sampleTested` properties:

```console
SAMPLED: 50 of 41,204 live object(s), strategy random, seed 7 — this run did NOT cover every object
```

That line is the feature. A sampled green result that reads like an
exhaustive green result is worse than no result, because somebody upgrades on
the strength of it — and a JUnit reporter showing fifty green tests is where
that mistake is easiest to make.

`--namespace` narrows a `--live` run to one namespace. Only the namespaced
object class is affected: on a claim-offering XRD the composites are
cluster-scoped, so there is nothing to narrow on that side.

Sampling interacts with `--concurrency` only in the obvious way — fewer
samples, less to parallelise.

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

### CI-native formats: `github`, `sarif`, `markdown`

`table`, `json` and `junit` all put a finding somewhere a person has to go
looking for it. These three put it on the line of the config that produced it.

| Format | Renders | Use it for |
|---|---|---|
| `github` | GitHub workflow commands (`::error file=…,line=…::…`) on stdout, plus a markdown table appended to `$GITHUB_STEP_SUMMARY` when the runner sets it | annotations on the pull-request diff |
| `sarif` | SARIF 2.1.0 | `github/codeql-action/upload-sarif` — findings land in code scanning, so they appear on the diff **and** in the Security tab, and can be triaged and suppressed like any other scanner's |
| `markdown` | a deterministic table | a PR comment in any CI system |

Available on `test`, `validate` and `analyze`; `diff` has `markdown` (its
delta is a structured comparison, not a finding list).

```console
$ convctl analyze --xrd xrd.yaml --config config.yaml -o github
::error file=config.yaml,line=13,col=7,title=Conversion config error::hub field "spec.size" is not covered by any rule and has no identical counterpart in the spoke schema
```

Locations come from a second, position-preserving parse of the config.
`sigs.k8s.io/yaml` routes through `encoding/json` — which is what makes the
strict typed decode possible and also what throws line numbers away — so the
formats read positions separately and never decide whether a config is valid.

A finding the tool cannot place precisely is still reported, against the file
with no line, or against the config's document. Dropping it would hide
whole-config errors, which are the most serious kind.

#### Finding ids

The `ruleId` in SARIF and the finding name in the tables are a compatibility
surface: a suppression in code scanning is keyed on the id, so renaming one
silently un-suppresses everything somebody dismissed. Ids are added, never
renamed.

| Id | Meaning |
|---|---|
| `convctl/unacknowledged-loss` | a round trip lost a field no rule declares lossy |
| `convctl/acknowledged-loss` | a declared, deliberate loss — reported at note severity, never a failure |
| `convctl/conversion-error` | a conversion failed outright |
| `convctl/schema-violation` | the converted object violates the destination schema (`--validate-output`) |
| `convctl/uncovered-field` | a schema field no rule claims |
| `convctl/rule-never-exercised` | a declared rule no sample reached |
| `convctl/golden-drift` | the committed corpus and the current output disagree |
| `convctl/config-error`, `convctl/config-warning` | a diagnostic with no more specific id |
| `convctl/required-field-*` | required-field analysis — the engine's own codes, lower-kebab |

```yaml
- run: convctl test --xrd xrd.yaml --config config.yaml --samples ./samples/ -o sarif > convctl.sarif
  continue-on-error: true
- uses: github/codeql-action/upload-sarif@v4
  with:
    sarif_file: convctl.sarif
```

`continue-on-error` on the first step is deliberate: the upload should happen
whether or not the run failed, or a red build hides the findings explaining
why it is red.

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

## `convctl lint`

One command, one exit code, over a whole tree.

```console
$ convctl lint ./platform/

convctl lint: 12 config(s), 12 schema(s)

STATUS  CONFIG                                  TARGET                  SCHEMA                        FINDINGS
OK      platform/apis/buckets/conversion.yaml   xbuckets.example.org    platform/apis/buckets/xrd.yaml  0
OK      platform/apis/widgets/conversion.yaml   widgets.example.org     platform/apis/widgets/crd.yaml  0

SUMMARY: 0 error(s), 0 warning(s), 0 unpaired, 0 duplicate
```

A repository with fifty XRDs otherwise needs fifty invocations, each pairing a
config with its schema by hand and each producing an exit code the caller has
to aggregate — which in practice means a bash loop in every consumer's CI,
written slightly differently each time.

`lint` walks the paths given (default `.`), recognises every
`XRDConversionConfig` and `CRDConversionConfig` **by its own `apiVersion` and
`kind`** rather than by filename — including multi-document files — pairs each
with the XRD or CRD whose `metadata.name` it targets, and runs the checks
`validate` and `analyze` run.

### Unpaired and duplicate configs are errors, never skips

A config paired with nothing looks exactly like a config that passed. So an
unpaired config is an **error** naming the target it looked for:

```console
ERROR  platform/apis/orders/conversion.yaml  xorders.example.org  —  1
  error  platform/apis/orders/conversion.yaml:1  no XRD named "xorders.example.org" was found in the tree, so this
                                                 config could not be checked against a schema; pass --schema-dir if
                                                 its schema lives elsewhere
```

A second config targeting the same resource is reported the same way. The
operator enforces one config per target, and finding that out from an
admission rejection after merge is what this command exists to prevent.

A manifest that is neither — a Deployment, a kustomization — is ignored rather
than rejected. A platform tree is full of files that are none of this
command's business.

### Offline by design

`lint` constructs **no Kubernetes client at all**. It is the fast check that
runs on every commit; [`test --live`](#convctl-test) is the slow one that runs
before merge.

### As a pre-commit hook

A [`.pre-commit-hooks.yaml`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/.pre-commit-hooks.yaml)
ships in the repository:

```yaml
repos:
  - repo: https://github.com/TeraSky-OSS/declarative-conversion-operator
    rev: v0.5.0
    hooks:
      - id: convctl-lint
```

The hook runs once over the tree rather than once per changed file: pairing a
config with its schema needs to see both, and a per-file hook would report
every config as unpaired.

### Flags and exit codes

| Flag | Meaning |
|---|---|
| `--schema-dir` | additional trees to search for XRDs and CRDs, when schemas live apart from configs (repeatable) |
| `--exclude` | glob patterns to skip, matched against the path and its base name (repeatable) |
| `--concurrency` | parallel workers (default one per CPU); the report order is the walk order regardless |
| `--fail-on` | `none` \| `warn` \| `loss` (default), the same matrix as `test` |

| Code | Meaning |
|---|---|
| 0 | clean at the chosen threshold |
| 1 | findings at or above it, or any unpaired/duplicate config |
| 2 | usage error |

## `convctl plan`

The ordered, gated path from where a target actually is to where you want it.

A safe version migration is a sequence — add the version, wire the spoke,
verify, promote the hub, retarget Compositions, migrate storage, prune
`storedVersions`, stop serving, drop the block — and every step has a
condition that has to hold before the next one is safe. That sequence exists
in this repository as prose spread across three documents and six example
directories. `plan` reads the target's current state and prints the path from
there, with each step carrying the command, the gate, and the command that
proves the gate.

```console
$ convctl plan --xrd xrd.yaml --config config.yaml --to v2

XRD: xwidgets.example.org
Config: xwidgets-conversion
Hub: v1 → v2

  1. [DONE] Serve v2 on the XRD
     run:     edit xwidgets.example.org: set spec.versions[name=v2].served: true (leave referenceable on v1 for now)
     gate:    v2 is served and the apiserver accepts reads at it
     verify:  kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{.spec.versions[?(@.name=="v2")].served}'

  2. [DONE] Wire v2 into the conversion config
     run:     convctl suggest --xrd <xrd.yaml> --hub v1 --spoke v2  # then apply the config
     gate:    the config declares a rule set covering v2, and `convctl validate` is clean
     verify:  convctl validate --xrd <xrd.yaml> --config <config.yaml>

▶ 3. [READY] Promote v2 to the hub
     run:      edit xwidgets.example.org: move referenceable: true from v1 to v2, and set spec.hubVersion: v2 in the config
     gate:     conversion is verified against real objects AND the webhook has reached the generated CRD. Applied is not the same as converting: until Crossplane re-renders the generated CRD, reads at a non-storage version return stored objects relabelled but UNCONVERTED, with HTTP 200 and no error
     verify:   convctl test --xrd <xrd.yaml> --config <config.yaml> --live --validate-output --verify-propagation --version-pair v1:v2
     blocked:  the hub is still v1

  4. [UNKNOWN] Retarget Compositions at the new hub
     run:      convctl retarget --xrd xwidgets.example.org --to v2
     gate:     every Composition's compositeTypeRef.apiVersion names v2, and every XR's compositionRef points at a retargeted Composition
     verify:   convctl retarget --xrd xwidgets.example.org --to v2 --dry-run
     blocked:  cannot be determined from files alone; confirm against the cluster with the verify command

  ...

  7. [BLOCKED] Stop serving v1
     run:      edit xwidgets.example.org: set spec.versions[name=v1].served: false (mark it deprecated first, with a deprecationWarning)
     gate:     v1 is not in status.storedVersions, no field manager is still writing it, and the object walk completed
     verify:   convctl versions --xrd <xrd.yaml> --check-unserve v1
     blocked:  step 3 (Promote v2 to the hub) has not been done yet

NEXT: step 3 — Promote v2 to the hub
```

`plan` is read-only. It prints; you run the steps.

### Why only one step is offered

Steps already satisfied are marked `DONE` rather than reprinted as work, and
exactly one outstanding step is ever marked `READY`. Everything after it is
`BLOCKED`, naming the step that blocks it.

That is deliberate. Presenting five satisfiable steps at once is how they get
done out of order, and for this particular sequence, out of order means
serving a version with no conversion behind it — which fails silently. The
plan is a queue, not a checklist.

### `UNKNOWN`, and why it is not `READY`

Three steps on an XRD — retargeting Compositions, migrating storage, pruning
`storedVersions` — have no answer in a manifest. (A native CRD has two: there
are no Compositions to retarget.) Whether every stored object
has been rewritten is a fact about the cluster.

Those steps are marked `UNKNOWN` rather than guessed at. `UNKNOWN` is neither
offered as the next step nor allowed to block the ones after it:

- Calling it `READY` would claim the tool checked something it did not.
- Calling it `BLOCKED` would stall every later step behind a gate that can
  never close from files, hiding the rest of the plan on a late-stage target.

Each one carries the `verify` command that answers it against a live cluster.
When no `READY` step remains, `plan` says so and reports how many steps still
need a cluster:

```console
Nothing outstanding that can be determined from files. 3 step(s) need a cluster to confirm — run their verify commands.
```

### Retirement waits for the cluster

Un-serving a version, and dropping its block, are the only irreversible steps
in the sequence — and whether either is safe rests entirely on the three
`UNKNOWN` steps above. So they are never offered as the next thing to do on
the strength of a manifest. They are printed, with their gates, marked
`BLOCKED` on the steps that need confirming:

```console
  7. [BLOCKED] Stop serving v1
     blocked:  step(s) 4, 5, 6 need a cluster to confirm and retiring a version is irreversible; run their verify commands, then re-run with --assume-verified
```

`--assume-verified` is you saying you ran those verify commands. It asserts
the cluster-only gates and nothing else — three of them on an XRD, two on a
native CRD — and every other step is still judged from the files.

```console
$ convctl plan --xrd xrd.yaml --config config.yaml --to v3 --assume-verified
...
NEXT: step 8 — Drop the v1 version block
```

### Package-managed XRDs are ordered differently

For an XRD shipped inside a Crossplane Configuration, the conversion config
must be applied **before** the package upgrade lands. That is the reverse of
the hand-applied order, and getting it backwards leaves the new version served
with no conversion at all — reads return stored objects relabelled but
unconverted, `200 OK`, no error anywhere.

`plan` detects this from the XRD's `ownerReferences` (a `ConfigurationRevision`
owner) and reorders accordingly, or you can force it with `--package-managed`:

```console
$ convctl plan --xrd xrd.yaml --config config.yaml --to v2 --package-managed
...
PACKAGE-MANAGED: the conversion config must be applied BEFORE the package upgrade lands,
                 or the new version is served with no conversion at all.
```

### Which versions get retirement steps

A version gets "stop serving" and "drop the block" steps when it is the hub
being replaced, when it is marked `deprecated`, or when it has already stopped
being served.

A served, undeprecated spoke gets neither. Keeping old versions readable is
the entire point of a conversion webhook; retiring one is a separate decision,
and you signal it by deprecating the version.

### Native CRDs

`--crd` plans the same sequence for a native CRD, where the hub is the
`storage: true` version. There is no Composition-retargeting step, because
nothing points a `compositeTypeRef` at a plain CRD.

```console
$ convctl plan --crd crd.yaml --config crdconversionconfig.yaml --to v2
```

### Output and exit codes

`--output json` emits the whole plan — every step with its `run`, `gate`,
`verify`, `status` and `blockedBy` — for a controller or a pipeline to consume.

| Code | Meaning |
|---|---|
| 0 | a plan was produced |
| 1 | the target state is unreachable (the version is not declared on the target) |
| 2 | usage error |

## `convctl versions`

One table that answers *is it safe to stop serving this version yet?*

(Stopping to serve it and removing its version block are two different
steps — the second is irreversible for anything still stored at it, and
[`convctl compat`](#convctl-compat) is the check for that one.)

Deciding that requires four facts that live in four different places: is
anything still stored at it, is anything still *writing* it, is it marked
deprecated, and does a spoke rule set exist for it.

```console
$ convctl versions --xrd xrd.yaml --config config.yaml

XRD: xwidgets.example.org	Config: widgets-conversion

VERSION  SERVED  HUB  DEPRECATED  SPOKE RULES  LIVE OBJECTS  STORED  LAST WRITTEN AT
v3       yes     yes  no          yes          412           yes     argocd @ 2026-06-01T09:14:02Z
v2       yes     no   no          yes          0             no      -
v1       yes     no   yes         yes          3             yes     legacy-reconciler @ 2026-05-30T22:10:44Z

v1 is deprecated: use v3; v1 will stop being served in the next release
```

| Column | Source |
|---|---|
| `SERVED` | XRD `spec.versions[].served` |
| `HUB` | `referenceable` / `storage` |
| `DEPRECATED` | `deprecated` + `deprecationWarning` |
| `SPOKE RULES` | whether the conversion config has a spoke entry |
| `LIVE OBJECTS` | instance count, across **both** generated CRDs on a claim-offering XRD — see the note below on what it does *not* mean |
| `LAST WRITTEN AT` | derived from each object's `managedFields[].apiVersion` |
| `STORED` | whether the version appears in `status.storedVersions` |

**`LIVE OBJECTS` is inventory, not evidence about that version.** The
apiserver converts on read, so listing at any served version returns *every*
object converted to it — the count is the same on every served row, and it is
not the number of objects stored at that version. It is therefore **not** a
blocker for `--check-unserve`: if it were, a single XR anywhere would block
un-serving every spoke forever. `STORED` is what answers the storage
question, and `LAST WRITTEN AT` answers who is still writing. Both are
genuinely per version.

**`LAST WRITTEN AT` is usually the one that decides.** "Nothing is stored at
v1" says the data has moved; it says nothing about the controller that still
PUTs v1 objects every reconcile and starts failing the moment the version
stops being served. Each object's `managedFields` records the apiVersion its
writers used, so the answer is already in the cluster — aggregated here by
manager, so the output says *who*, not just *that*.

Crossplane's own `deprecated` / `deprecationWarning` are per-version fields
copied into the generated CRD by `xcrd.genCrdVersion`, and nothing in this
project surfaced them until now.

A count followed by `+` hit the sample bound (`--max-samples`); a count
followed by `?` means the objects at that version **could not be listed** —
RBAC, a timeout, anything that is not "the apiserver does not serve this
version". The row then says so underneath, and `--check-unserve` treats it as
a blocker. This is the one command where mistaking *"I could not look"* for
*"there are none"* unserves a version the fleet is still reading.

### As a gate on un-serving

`--check-unserve` gates the `served: false` flip, not the removal of the
version block. Passing it does not mean the version can be deleted.

```console
$ convctl versions --xrd xrd.yaml --check-unserve v1

v1 is NOT safe to stop serving:
  - it appears in status.storedVersions, so the apiserver believes objects are still persisted at it — run `convctl migrate-storage` first
  - still actively written by: legacy-reconciler
```

Exits non-zero with the reasons, so it drops into a pipeline or into
[`convctl plan`](#convctl-plan)'s gate for the un-serve step.

Read-only throughout. The object walk paginates and is bounded by
`--max-samples` (default 5000): asking whether a version is safe to drop
should not be a way to take the apiserver down. A count shown as `N+` means
the bound was hit, and an incomplete walk is itself reported as a blocker
rather than being allowed to read as "nothing is there".

## `convctl compat`

A single command a branch-protection rule can require: *you may not merge a
change that breaks an existing conversion without acknowledging it.*

`convctl diff` compares two configs against one schema. `compat` compares
**schema + config at two points in time** and classifies the delta by
severity.

```console
$ convctl compat --base origin/main --head HEAD --config config.yaml --xrd xrd.yaml
Compatibility: origin/main → HEAD (config.yaml)

SEVERITY  CLASS          DETAIL
breaking  CoverageLost   spoke v1: "spec.tier" had a rule and no longer does
breaking  RuleRemoved    spoke v1: rule 1 (FieldRename) was removed and no replacement claims its paths

RESULT: breaking changes found. Acknowledge a deliberate one with --allow <class>.
```

| Class | Severity | Meaning |
|---|---|---|
| `LosslessToLossy` | breaking | a direction that used to round-trip no longer does |
| `CoverageLost` | breaking | a field that had a rule no longer has one |
| `ServedVersionRemoved` | breaking | a version dropped while objects may still be stored at it |
| `RuleRemoved` | breaking | a rule deleted without a replacement claiming its paths |
| `HubChanged` | breaking | the hub moved — legitimate, but must be deliberate |
| `StrategyChanged` | review | same paths, different strategy |
| `MappingChanged` | review | same paths and strategies, wired to each other differently — two renames that swapped destinations change every object while leaving every aggregate identical |
| `CoverageGained` | safe | a field is newly covered |
| `NewVersion` | safe | a version is newly served |

`--allow <class>` (repeatable) acknowledges a class **deliberately**, so a
real hub promotion passes the gate with an explicit flag rather than by
switching the check off. The output names which acknowledgements were used,
and acknowledging one class does not acknowledge the others.

Revisions are read with `git show <ref>:<path>`, so no checkout is needed and
the gate works in a shallow CI clone (`fetch-depth: 2`). Paths are the ones
you would type into any other `convctl` command — relative to your working
directory, not to the repository root — so running `compat` from inside the
directory that holds the config works the same as running it from the top.

If either revision does not analyze cleanly, the report says so as a `NOTE`
above the table. Those are not compatibility changes — an uncovered field or
an unserved spoke is ordinary currency here, and several of them are exactly
what the classes above describe — but *"No differences"* between two
revisions that both fail `convctl validate` reads as a pass, so the
comparison names the side it could not fully analyze.

`--output markdown` is stable between runs on the same input — no timestamps,
fixed ordering — so a sticky PR comment updates in place instead of producing
a fresh diff on every CI run.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | no unacknowledged breaking changes |
| 1 | breaking changes found |
| 2 | usage, or a revision that could not be resolved |

### As a required status check

```yaml
- uses: actions/checkout@v7
  with:
    fetch-depth: 2          # compat needs the base revision, not the history
    persist-credentials: false
# checkout fetches only the triggering ref, so origin/<base> does not exist
# yet. This refspec is what creates it; one commit is enough.
- run: |
    git fetch --depth=1 origin \
      "+refs/heads/${{ github.base_ref }}:refs/remotes/origin/${{ github.base_ref }}"
- run: |
    convctl compat \
      --base "origin/${{ github.base_ref }}" --head HEAD \
      --config config.yaml --xrd xrd.yaml \
      --output markdown | tee compat.md
```

Make that job a required check and a breaking conversion change cannot merge
without somebody adding `--allow` and saying why in the PR.

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

## `convctl rehub`

Drafts a conversion config rewritten so `--to` becomes the hub. `--to` must
already be a spoke (add it first, then promote). Prints a same-kind YAML/JSON
object to stdout and **never applies** it.

```console
convctl rehub --config config.yaml --xrd xrd.yaml --to v3
convctl rehub --config config.yaml --crd crd.yaml --to v2 -o yaml
```

| Flag | Description |
|---|---|
| `-c, --config` | Path to an `XRDConversionConfig` or `CRDConversionConfig`. **Required.** |
| `-x, --xrd` / `--crd` | Target schema. Exactly one required (matched to the config's `kind`). |
| `--to` | Version that becomes the new hub — must already be a spoke. **Required.** |
| `-o, --output` | `yaml` (default) or `json`. |
| `--allow-invalid` | Print the draft even if `Analyze` reports it Invalid. |

What it does:

1. Sets `hubVersion` to `--to`
2. Drops the `--to` spoke entry
3. Adds the **old hub** as a spoke whose rules are the **invert** of the old `--to` rules (e.g. `FieldRename` path swap, `ToAnnotation` → `FromAnnotation`)
4. For every other spoke, **composes** old rules through the old-hub → new-hub path map (and lifts previously auto-covered renames when the spoke still has that leaf)

Hub promotion is **not** a mechanical path swap — remaining spokes must be
re-expressed against the new hub. `rehub` fail-closes if a rule cannot be
rewritten safely (CEL and JSONPatch cannot be used as a hub path map when
promoting, remaining-spoke CEL/JSONPatch that need path rewrite fail closed,
and `ToLabel` with `serialization: JSON` cannot invert). Review the draft, then
`convctl validate` / `convctl test` before applying.

The Crossplane lifecycle example is the acceptance fixture:

```console
# stage 02 → matches stage 03
convctl rehub --config examples/crossplane-xr-multiversion/02-add-v2/xrdconversionconfig.yaml \
  --xrd examples/crossplane-xr-multiversion/02-add-v2/xrd.yaml --to v2

# stage 04 → matches stage 05 (v1 gets two FieldRenames)
convctl rehub --config examples/crossplane-xr-multiversion/04-add-v3/xrdconversionconfig.yaml \
  --xrd examples/crossplane-xr-multiversion/04-add-v3/xrd.yaml --to v3
```

See [Changing the hub version](configuration/xrdconversionconfig.md#changing-the-hub-version)
for the in-cluster promote sequence (`referenceable` / Composition retarget).

## `convctl generate kyverno`

Drafts two [`policies.kyverno.io/v1` MutatingPolicies](https://kyverno.io/docs/policy-types/mutating-policy/)
that retarget existing Crossplane XRs onto a new hub Composition. XRD-only
(native CRDs have no Composition). Prints YAML/JSON to stdout and **never
applies** it.

```console
convctl generate kyverno --xrd xrd.yaml --to v2
convctl generate kyverno --xrd xrd.yaml --from v1 --to v2
```

| Flag | Description |
|---|---|
| `-x, --xrd` | Path to an XRD YAML file. **Required.** Group + kind scope the Composition labeler; group, plural, and every served version fill the migrate policy's `matchConstraints`. |
| `--to` | Target `xrd-api-version` label. Must be a version on the XRD. **Required.** |
| `--from` | Optional canary: only migrate XRs whose selector is missing or equals this version. Without `--from`, anything not already labeled `--to` is migrated. |
| `--label-key` | Label key (default `xrd-api-version`). |
| `--composition-policy-name` | Name of the Composition labeler (default `label-compositions-<plural>`). |
| `--migrate-policy-name` | Name of the XR migrate policy (default `set-composition-version-selector-<plural>`). Same object on every hub flip — update `--from` / `--to`, do not create a new policy. |
| `-o, --output` | `yaml` (default, multi-doc) or `json`. |

**Document 1 — per-XRD Composition labeler.** Admission writes
`xrd-api-version` from the version element of `compositeTypeRef.apiVersion`
(`example.org/v2` → `v2`). **Do not** put that label on Composition YAML in
git — Kyverno is the source of truth. XRD targeting (kind + group) lives in
the mutation CEL; Kyverno 1.18 silently ignores `matchConditions` that read
`object.spec`. Not a cluster-wide catch-all: XRDs that never evolve their API
do not need this policy. Admission and `mutateExisting` are on.

**Document 2 — standing XR migrate policy** (`set-composition-version-selector-<plural>`).
One object per XRD. Re-generate with the new `--from` / `--to` on a hub flip
and apply the same `metadata.name` — do not create `migrate-*-to-vN`.
Admission and `mutateExisting` are on. Removes `spec.crossplane.compositionRef`
and `compositionRevisionRef`, then sets `compositionSelector.matchLabels` to
`--to`. Extra admin selector keys are left intact. Crossplane then re-selects;
`Automatic` writes a new revision pin. That write also persists the XR at the
new `referenceable` version. Admission is the path that works on Kyverno
1.18.1 — `mutateExisting` alone never creates UpdateRequests
([kyverno#16255](https://github.com/kyverno/kyverno/pull/16255)), so
already-existing XRs need a write (re-apply or annotate) after the policy
lands. UPDATE matches on `oldObject` so a later pin write does not rematch.

Crossplane pins `compositionRef` at create time and ignores the selector until
that pin is removed. `compositionUpdatePolicy: Automatic` only walks revisions
of the already-pinned Composition. **Do not** use XRD `enforcedCompositionRef`
to chase hub versions — that field is immutable.

If more than one Composition matches the selector after the pin is cleared,
Crossplane picks at random. A version-only selector is safe only when there is
one Composition per hub version.

A worked apply order lives in
[`examples/crossplane-xr-multiversion/gitops/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/gitops).
Run it with the main demo: `./examples/crossplane-xr-multiversion/demo.sh --demo-mode gitops`
(add `--gitops-engine flux|argo` for a live GitHub + in-cluster runner walkthrough).

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

## `convctl migrate-storage`

Rewrites every live instance of a target XRD or CRD so etcd stores it at the current storage version. This is the first **mutating** `convctl` command: `test --live` / `diff --live` are read-only, and `patch-preview` never constructs a client.

`--xrd` and `--crd` here are **cluster resource names**, not local YAML files. No conversion config is required. The live schema is the source of truth for GVK, scope, storage version, and `status.storedVersions`.

**A claim-offering XRD has two CRDs, and both are migrated.** A `scope: LegacyCluster` XRD with `spec.claimNames` generates the cluster-scoped composite CRD *and* a namespace-scoped claim CRD (`{claimPlural}.{group}`). Claims are their own stored object class with their own `status.storedVersions`, so migrating the composite alone left the old version un-droppable — which is the entire reason to run `--prune-stored-versions`. Both are now listed, rewritten, and pruned, and the report breaks the counts down per CRD.

A failure on **either** CRD blocks the prune on **both**, naming which one failed. A half-pruned pair still cannot drop the version, but has already thrown away the record of which objects were stored at it.

After you promote a new storage version (`storage: true` on a CRD, `referenceable: true` on an XRD), objects already in etcd stay physically encoded at whichever version was storage when they were last written — **unless something writes them again**. The apiserver serves them correctly either way, but Kubernetes rejects dropping an old version from the CRD/XRD until `status.storedVersions` no longer lists it.

**XRD vs CRD — this command is not equally urgent.**

- **XRDs.** Crossplane will not let a Composition's `compositeTypeRef` change, so promoting the hub means a *new* Composition and a write of every existing XR (a `compositionRef` name patch, or the GitOps [`generate kyverno`](#convctl-generate-kyverno) migrate policy that strips the pin and re-selects). That write persists the object at the new `referenceable` version, so etcd is usually already rewritten by the time you deprecate an old spoke. What still blocks dropping the version block is the generated CRD's `status.storedVersions`, which never shrinks on its own. `--prune-stored-versions` is the step that matters; the empty SSA pass is belt-and-suspenders (XRs you forgot to retarget, or anything that was never patched).
- **CRDs.** Flipping `storage: true` does **not** write existing objects. There is no Crossplane-style retarget. Empty SSA is the actual etcd rewrite, and skipping it leaves CRs encoded at the old version indefinitely. This is the critical path.

`migrate-storage` does the rewrite with an **empty server-side-apply patch** (`apiVersion`, `kind`, `metadata.name`, and `metadata.namespace` only) under a dedicated field manager, with force-conflicts. The apply claims only identity fields; the write still goes through the persist path, so etcd is re-encoded at the current storage version. Conversion webhooks — including this operator — run as they would on any write.

This is **not** the Kubernetes 1.30+ [`StorageVersionMigration`](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/#upgrade-existing-objects-to-a-new-stored-version) API and does not need the storage-version-migrator. It is the standard empty-SSA approach that works on any supported cluster (Kubernetes 1.27+; Server-Side Apply is required). A second run with the same field manager is a no-op once objects are already stored at the current version.

```console
convctl migrate-storage --xrd xwidgets.example.org
convctl migrate-storage --crd widgets.example.org
convctl migrate-storage --xrd xwidgets.example.org --dry-run
convctl migrate-storage --crd widgets.example.org --prune-stored-versions
```

| Flag | Description |
|---|---|
| `-x, --xrd` | Cluster name of the `CompositeResourceDefinition`. Mutually exclusive with `--crd`; exactly one is required. **Not a file path.** |
| `--crd` | Cluster name of the `CustomResourceDefinition`. **Not a file path.** |
| `--kubeconfig` | Path to a kubeconfig file. Resolves exactly like `kubectl`. |
| `--context` | Kubeconfig context to use. |
| `-n, --namespace` | Limit to this namespace. Default: all namespaces. Ignored (with a warning) for cluster-scoped types — on a `LegacyCluster` XRD that is the **composite** side, so `--namespace` narrows only its claims. Refused with `--prune-stored-versions`. |
| `--dry-run` | Same Apply call with server-side dry-run (`DryRun: All`) — exercises conversion, does not persist. Also skips `--prune-stored-versions`. |
| `--concurrency` | How many objects to patch in parallel. Defaults to **1** (this is a write). |
| `--field-manager` | SSA field manager. Defaults to `convctl`. Always applied with force-conflicts. |
| `--prune-stored-versions` | After **every** object apply succeeds, set the generated/native CRD's `status.storedVersions` to the current storage version only. Skipped (with a warning) if any object failed, or with `--dry-run`. Refused with `--namespace` on a namespaced type: other namespaces may still store an older version. |
| `-o, --output` | `table` (default) or `json`. |
| `--quiet` | Suppress the progress line written to stderr. |

For an XRD, the command reads the XRD, then the generated CRD (`{plural}.{group}`) for `status.storedVersions` and as a cross-check of `storage: true`. If the XRD's `referenceable` version and the CRD's storage version disagree, the CRD wins (that is what etcd stores) and a warning is printed.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Every object apply succeeded (and prune, if requested). |
| `1` | One or more object applies failed, or a requested prune failed. Remaining objects are still attempted. |
| `2` | Usage or cluster/schema error (bad flags, `--prune-stored-versions` with `--namespace`, missing XRD/CRD, list failed, no/multiple storage versions). |

### RBAC

The invoking identity — not the operator's ServiceAccount — needs:

- `get` on the target XRD and/or CRD
- `list` and `patch` on the target CRs/XRs
- `update` on `customresourcedefinitions/status` only if you pass `--prune-stored-versions`

## Pre-upgrade checks: testing against everything that already exists

`--samples` is for hand-written fixtures. `--live` sources samples from a real cluster instead — every existing instance of **every** resource type the target generates, fetched at its hub/storage version (so it works even *before* any conversion webhook is wired up, since the storage version is always readable). For a `CRDConversionConfig` that is the native CRD's own type. For an `XRDConversionConfig` it is the composite type and, on a `scope: LegacyCluster` XRD with `spec.claimNames`, its **claims** as well — claims carry the same `spec.conversion` and are built from the same authored schema, so they go through the very same webhook, and sampling composites alone silently covered roughly half the objects a pre-upgrade check is supposed to cover:

```console
convctl test --xrd xrd.yaml --config new-config.yaml --live
convctl test --crd crd.yaml --config new-crd-config.yaml --live \
  --kubeconfig ~/.kube/other-config --context prod
```

When more than one CRD contributes, the report breaks the sample count down per CRD, each sample records which CRD it came from (`crd` / `crdRole` in JSON), and every JUnit `<testcase>` carries `crd` and `crdRole` properties — so a claim-side failure is distinguishable from a composite-side one without parsing case names.

This is the tool to run before applying a new or changed `XRDConversionConfig`/`CRDConversionConfig`: does it hold up against every object that already exists in the cluster, not just your fixtures? `--kubeconfig`/`--context` resolve exactly like `kubectl` does. The invoking identity only needs `get`/`list` on the target resource type — no write access, and nothing related to this operator's own CRDs or webhook server.

To run the same pair of checks (`diff --live` + `test --live`) against every
cluster in a fleet before merge, see [Fleet CI](gitops/fleet-ci.md).
`convctl test --xrd xrd.yaml --config proposed.yaml --live --contexts east,west -o junit`
produces one JUnit document with a `<testsuite>` per cluster (a cluster
that cannot be reached is an `<error>` suite, not a silent skip).

## Shell completion

`convctl completion [bash|zsh|fish|powershell]` (built into Cobra) prints a completion script for your shell:

```console
source <(convctl completion bash)
```

See `convctl completion --help` for how to install it permanently for your shell.

Once installed, flags complete as follows:

- `--xrd` / `--crd` / `--config` / `--sample` on offline commands complete YAML files; `--samples` completes directories.
- `migrate-storage --xrd` / `--crd` complete **cluster resource names** (XRDs and CRDs listed from the current kubeconfig context), not files. `--namespace` completes live namespaces the same way.
- `--context` on `test`, `diff`, and `migrate-storage` completes kubeconfig context names. `--contexts` on `test` uses the same list. `--kubeconfig` already on the command line is honored, so `convctl migrate-storage --kubeconfig ./other --context <tab>` lists contexts from that file.
- `--output` and `test --fail-on` complete their allowed values.

Cluster lookups during completion time out after two seconds and fall back to no suggestions if the apiserver is unreachable, so a hung cluster cannot freeze tab-complete.

## `convctl retarget`

Points every live object of an XRD at the Compositions labelled for a version — the Kyverno-free path through the retarget step in the [XR lifecycle](examples/xr-lifecycle.md). Like `migrate-storage`, this is **live and mutating**, and `--xrd` is a cluster resource name rather than a file.

Promoting an XRD's hub version means a **new** Composition: Crossplane will not let a Composition's `compositeTypeRef` change. Existing XRs stay pinned to the old one until something rewrites them. For each object, `retarget` clears the pin (`compositionRef`, `compositionRevisionRef`) and sets `compositionSelector.matchLabels` to the target version. Crossplane re-selects — and that write also persists the object at the new `referenceable` version, which is usually what makes `migrate-storage`'s empty-SSA pass a no-op afterwards.

```console
convctl retarget --xrd xwidgets.example.org --to v2 --dry-run
convctl retarget --xrd xwidgets.example.org --to v2 --canary 10%
convctl retarget --xrd xwidgets.example.org --to v2
```

!!! warning "Once the pin is cleared, Crossplane picks at random"
    This is the same caveat [`generate kyverno`](#convctl-generate-kyverno) documents. With `compositionRef` gone, Crossplane selects among **every** Composition the selector matches, arbitrarily. A version-only selector is therefore safe only if there is exactly one Composition per hub version. Label your Compositions accordingly, or keep pinning with a richer selector instead of using this command.

**It is scope-aware, and it has to be.** Under `scope: Namespaced` / `Cluster` the machinery sits under `spec.crossplane`; under `LegacyCluster` — and on every **claim**, whatever the scope — it sits directly under `spec`. Patching the wrong subtree would silently create a field Crossplane never reads and report success, so a scope the resolver cannot determine is a hard refusal rather than a guess. A `LegacyCluster` XRD's claims are retargeted alongside its composites.

**Why a merge patch, not Server-Side Apply.** `migrate-storage` uses SSA and this command otherwise mirrors it, but SSA can only *remove* a field the applying manager already owns — and `compositionRef` is owned by whoever pinned it. An apply that simply omits the field leaves it in place, which would leave every object still pinned to its old Composition: exactly what this command exists to undo. A merge patch with an explicit `null` removes it regardless of ownership, in the same request that sets the new selector.

| Flag | Description |
|---|---|
| `-x, --xrd` | Cluster name of the `CompositeResourceDefinition`. **Not a file path.** Required. |
| `--to` | The version to retarget onto. Must be a version the XRD declares. Required. |
| `--dry-run` | Sends the same patch with `DryRun: All` — exercises conversion and admission, persists nothing. |
| `--canary` | Retarget only the first `N` objects, or `N%` of them (e.g. `25`, `10%`). Selection is a prefix of the listing order, which the apiserver returns sorted by name, so a re-run with the same value picks the same objects. A non-zero percentage of a non-empty population always selects at least one. The selection is reported, so a partially-migrated fleet never looks like a completed one. |
| `--concurrency` | Objects to patch in parallel. Default **1** — this is a write. |
| `--label-key` | The Composition label key the selector matches on. Defaults to `xrd-api-version`, the same key `generate kyverno` uses. |
| `--field-manager` | Field manager recorded on the patch. Default `convctl-retarget`. |
| `--kubeconfig`, `--context` | Resolve exactly like `kubectl`. |
| `-o, --output` | `table` (default) or `json`. |
| `--quiet` | Suppress the progress line on stderr. |

An object that already selects the target version and carries no pin is reported as **already on target** and no patch is sent at all, so a re-run is visibly a no-op.

**Exit codes** follow the `migrate-storage` matrix: `0` success, `1` at least one object failed, `2` usage error.

**RBAC:** the invoking identity needs `get` on the XRD, `get`/`list` on the generated CRDs and Compositions, and `list`/`patch` on the XR (and claim) types.

## `convctl crossplane status`

Read-only. One screen answering *"where is my migration right now?"*.

```console
convctl crossplane status xwidgets.example.org
convctl crossplane status xwidgets.example.org -o json
```

The information all exists today — it is just spread across the XRD, the Compositions, every XR's `compositionRef`, both generated CRDs' `status.storedVersions`, and the `XRDConversionConfig`'s conditions, so assembling it by hand means half a dozen `kubectl` invocations and some arithmetic.

What it shows:

- **Per version:** `served`, `referenceable`, `deprecated` (and the XRD's own `deprecationWarning`), whether a spoke rule set covers it, and how many live objects read back at it. A version that is **served with no rule set** is called out explicitly — that is the shape that breaks reads.
- **Per generated CRD** (two of them on a `LegacyCluster` XRD with `claimNames`): scope, `spec.conversion.strategy`, storage version, `status.storedVersions`, and object count. `strategy: None` here while the XRD says `Webhook` is the propagation gap — see [`ConversionPropagated`](configuration/xrdconversionconfig.md#applied-is-not-the-same-as-conversion-works).
- **Per Composition** targeting this XRD: which `compositeTypeRef` version it declares, and how many XRs are pinned to it by name. Plus a count of objects with no pin at all, which Crossplane selects for by label.
- **The conversion config's** phase and every condition, including `ConversionPropagated` and `PackageManaged`.

It constructs no write client and issues no write of any kind.

**RBAC:** `get`/`list` on the XRD, the generated CRDs, the XR and claim types, Compositions, and `XRDConversionConfig`s.
