# Fleet CI: `convctl test` and `diff` across kubecontexts

Each cluster runs its own operator install
([Architecture: One cluster, one install](../architecture.md#one-cluster-one-install)).
The operator does not sync conversion state between clusters. Before you
merge a config change, run the same two read-only checks against **every**
cluster that will apply that YAML:

1. `convctl diff --live` — what would applying this file change on that
   cluster (coverage, rule claims, lossiness).
2. `convctl test --live` — does the proposed mapping still hold up against
   every live object of the target type.

Neither command writes to the cluster. The invoking identity only needs
`get`/`list` on the target XRD/CRD and its instances.

## Lint on commit, test before merge

Two checks, two speeds. `convctl lint` is offline — it constructs no
Kubernetes client — so it belongs on every commit, as a pre-commit hook and as
the first job in CI:

```console
convctl lint ./platform/
```

It pairs every conversion config in the tree with the XRD or CRD it targets
and reports an unpaired or duplicated config as an error rather than skipping
it. See [`convctl lint`](../cli.md#convctl-lint).

`convctl test --live` is the slow one, and the one that needs credentials for
every cluster. Run it before merge, not on every commit.

## Built-in: `convctl test --live --contexts`

Once you have more than one context in a single kubeconfig:

```console
convctl test --xrd xrd.yaml --config proposed.yaml --live \
  --contexts kind-fleet-a,kind-fleet-b \
  --output junit --output-file fleet.junit.xml
```

`--kubeconfig-dir ./clusters/` is the same idea when each cluster has its
own kubeconfig file. One context or one file keeps the existing
single-cluster report. A connection error on one cluster is recorded as a
failed suite; the others still run.

`convctl diff` stays one cluster per invocation (`--context`). The
[shell loop](#shell-loop-non-github-ci) still wraps both commands when you want
`diff --live` in the same gate.

## Shell loop (non-GitHub CI)

For Tekton, GitLab, Jenkins or anything else, [`convctl-fleet.sh`](convctl-fleet.sh) is a copy-pasteable wrapper. It
walks `CONTEXTS` (space-separated kubeconfig context names), writes one
JUnit file per cluster, and exits non-zero if any cluster failed.

```console
# Two kind clusters sharing ~/.kube/config:
export CONTEXTS="kind-fleet-a kind-fleet-b"
export CONVCTL_XRD=examples/field-rename/xrd.yaml
export CONVCTL_CONFIG=examples/field-rename/xrdconversionconfig.yaml
./docs/gitops/convctl-fleet.sh
```

`KUBECONFIG` / `--kubeconfig` resolve the same way `kubectl` does. To use
one kubeconfig file per cluster instead of contexts, set `KUBECONFIGS` to
a list of paths (the script uses each file's `current-context`).

## GitHub Actions

Three first-party Actions cover the fleet pattern, so the workflow is
configuration rather than a copy-pasted script:

```yaml
- uses: terasky-oss/declarative-conversion-operator/.github/actions/convctl-fleet@v1
  with:
    config: apis/widgets/conversion.yaml
    xrd: apis/widgets/xrd.yaml
    contexts: prod-us,prod-eu
    kubeconfig: ${{ secrets.FLEET_KUBECONFIG }}
```

One aggregated JUnit report with a `<testsuite>` per cluster, a summary
table, and a cluster that could not be reached recorded as a **failed suite**
rather than silently skipped.

| Action | Does |
|---|---|
| [`setup-convctl`](https://github.com/TeraSky-OSS/declarative-conversion-operator/tree/main/.github/actions/setup-convctl) | installs a cosign-verified `convctl` |
| [`convctl-test`](https://github.com/TeraSky-OSS/declarative-conversion-operator/tree/main/.github/actions/convctl-test) | one cluster or fixtures: JUnit artifact, job summary, diff annotations, and — with `comment: true` — a sticky pull-request comment |
| [`convctl-diff`](https://github.com/TeraSky-OSS/declarative-conversion-operator/tree/main/.github/actions/convctl-diff) | the coverage delta as a sticky pull-request comment |
| [`convctl-fleet`](https://github.com/TeraSky-OSS/declarative-conversion-operator/tree/main/.github/actions/convctl-fleet) | every cluster, one aggregated report |

### Putting the report on the pull request

`convctl-test` writes annotations and a job summary by default; set
`comment: true` to also upsert the report as a sticky comment, updated in
place on each push rather than appended:

```yaml
- uses: TeraSky-OSS/declarative-conversion-operator/.github/actions/convctl-test@main
  with:
    config: conversion/conversion.yaml
    xrd: platform/xrd.yaml
    live: "true"
    comment: "true"
```

It is off by default because annotations already put a failure on the diff,
and a comment on every push is noise for repos that do not want one. Turn it
on where the report *is* the review artifact — a platform repo where the
person approving the change is not the person who ran the tool.

The comment carries the **full report** — the run header, the per-path
table, rule coverage and the summary line — the same thing you would read in
a terminal. (`convctl test --output markdown` is deliberately not what gets
posted: it renders findings only, so a clean run would comment "No findings"
and tell a reviewer nothing about what was covered.)

A config that does not compile exits before any conversion is attempted and
writes its reason to stderr. That run still comments, with the tool's own
error, because it is the one a reviewer most needs to read. Set
`comment-title` when one workflow runs the Action twice, so the fixture run
and the live run do not produce two identically-headed comments.

Both this and `convctl-diff` talk to the REST API with `curl` and `jq`
rather than the `gh` CLI, so they work on a slim self-hosted runner image —
`ghcr.io/actions/actions-runner` ships those two and not `gh`. A comment
that cannot be posted (a fork's read-only token, or a workflow without
`pull-requests: write`) is a warning, not a failed job.

[`convctl-fleet.gha.yml`](convctl-fleet.gha.yml) is the full reference
workflow, built on those Actions. It runs as written — the only things to
change are the `context` matrix, the paths, and the kubeconfig secret. Copy
it into your platform repo.

`fail-fast: false` on a per-cluster matrix is still required: a red cluster
must not hide the others.

```yaml
strategy:
  fail-fast: false
  matrix:
    context: [prod-us, prod-eu, staging]
```

`fail-fast: false` is required: a red cluster must not hide the others.

## What "pass" means

| Command | Exit 0 | Exit 1 | Exit 2 |
|---|---|---|---|
| `convctl diff --live` | Cluster config and the file are equivalent | Any coverage/claim/lossy delta | Usage or cluster error |
| `convctl test --live` | Every path passed, or every loss was already `acknowledgeLossy` | Unacknowledged loss or conversion error | Usage or cluster error |

A fleet gate should fail the PR if **any** cluster's `test --live`
returns 1 or 2, or if `diff --live` returns 2 (usage / cannot reach the
cluster). A `diff` exit 1 is a coverage/claim delta — the change you are
about to roll out — so the reference script and workflow treat it as a
review artifact by default. Set `FAIL_ON_DIFF=1` when you want that
delta to fail the gate.

## Two-cluster check

Verify the loop against two kind clusters that share one kubeconfig:

```console
kind create cluster --name fleet-a
kind create cluster --name fleet-b
# install the operator + apply the field-rename XRD on both, then:
CONTEXTS="kind-fleet-a kind-fleet-b" \
  CONVCTL_XRD=examples/field-rename/xrd.yaml \
  CONVCTL_CONFIG=examples/field-rename/xrdconversionconfig.yaml \
  ./docs/gitops/convctl-fleet.sh
```

## Related

- [CLI: pre-upgrade checks](../cli.md#pre-upgrade-checks-testing-against-everything-that-already-exists)
- [CLI: `convctl diff`](../cli.md#convctl-diff)
- [Upgrade runbook](../operations/upgrade-runbook.md)
- XRD lifecycle GitOps demo (Flux/Argo + in-cluster `convctl test --live`):
  [`examples/crossplane-xr-multiversion/gitops/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/gitops)

## Branch protection: refusing a breaking conversion change

`convctl compat` classifies the delta between two revisions, so a required
status check can refuse a change that breaks an existing conversion:

```yaml
name: Conversion compatibility
on: pull_request

permissions:
  contents: read

jobs:
  compat:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          # compat resolves both revisions with `git show`, so it needs the
          # base commit but not the history.
          fetch-depth: 2
          persist-credentials: false
      - name: Fetch the base branch
        # checkout fetches only the ref that triggered the run, so
        # origin/<base> does not exist yet. The explicit refspec is what
        # creates the remote-tracking ref --base names; one commit is enough.
        run: |
          git fetch --depth=1 origin \
            "+refs/heads/${{ github.base_ref }}:refs/remotes/origin/${{ github.base_ref }}"
      # A pinned release with its signature verified, rather than
      # `go install ...@latest`, which resolves to whatever is newest when
      # the job runs and checks nothing about what it got.
      - uses: terasky-oss/declarative-conversion-operator/.github/actions/setup-convctl@v1
        with:
          version: v0.5.0
      - run: |
          convctl compat \
            --base "origin/${{ github.base_ref }}" --head HEAD \
            --config config.yaml --xrd xrd.yaml
```

Exit 1 on a breaking change, 0 once it is acknowledged with `--allow <class>`.
Acknowledging is per class, so allowing a deliberate hub promotion does not
also allow a dropped rule. See [the class table](../cli.md#convctl-compat).
