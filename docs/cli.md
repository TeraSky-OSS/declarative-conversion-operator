# CLI Reference: `convctl`

`convctl` runs the exact same `pkg/engine` code the operator and webhook server use, entirely offline against local YAML files — so you can validate and test a conversion mapping before it ever touches a cluster. Most commands work identically against an `XRDConversionConfig` (pass `--xrd`) or a `CRDConversionConfig` (pass `--crd`) — which one applies is determined by the config file's own `kind`, not by which flag you happen to type, so passing the wrong one is a clear error rather than a silent mismatch. `migrate-storage` is the exception: it is a live, mutating housekeeping command that takes cluster resource names (not files) and does not need a conversion config.

```console
convctl validate      --config config.yaml [--xrd xrd.yaml | --crd crd.yaml] [-o table|json]
convctl analyze       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json]
convctl test          --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) (--samples ./samples/ | --live) [flags]
convctl diff          --config a.yaml --config b.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o table|json]
convctl diff          --config config.yaml --live [-o json|table]
convctl convert       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) --sample obj.yaml --to v2 [-o yaml|json]
convctl suggest       --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) [-o yaml|json]
convctl rehub         --config config.yaml (--xrd xrd.yaml | --crd crd.yaml) --to v3 [-o yaml|json]
convctl generate kyverno --xrd xrd.yaml --to v2 [--from v1] [-o yaml|json]
convctl patch-preview --config config.yaml --service-name NAME --service-namespace NS --ca-bundle B64 [flags]
convctl migrate-storage (--xrd NAME | --crd NAME) [flags]
```

Roughly in the order you reach for them while authoring a mapping: `suggest` drafts rules for fields nothing covers yet, `validate` and `analyze` check the config statically, `convert` shows what a single object turns into, `test` grades fixtures or every live object, `diff` reports what a config edit changed, and `patch-preview` shows the exact patch the operator will apply once you commit. After a hub/storage-version promotion, `migrate-storage` rewrites live objects (critical for native CRDs; on XRDs the `compositionRef` retarget usually already did, and the remaining job is pruning `storedVersions`). For a GitOps hub flip, `generate kyverno` drafts MutatingPolicies that retarget existing XRs without a per-object name patch.

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
| `-n, --namespace` | Limit to this namespace. Default: all namespaces. Ignored (with a warning) for cluster-scoped types. |
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

`--samples` is for hand-written fixtures. `--live` sources samples from a real cluster instead — every existing instance of the target XRD's generated composite resource type (or, for a `CRDConversionConfig`, the native CRD's own resource type), fetched at its hub/storage version (so it works even *before* any conversion webhook is wired up, since the storage version is always readable):

```console
convctl test --xrd xrd.yaml --config new-config.yaml --live
convctl test --crd crd.yaml --config new-crd-config.yaml --live \
  --kubeconfig ~/.kube/other-config --context prod
```

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
