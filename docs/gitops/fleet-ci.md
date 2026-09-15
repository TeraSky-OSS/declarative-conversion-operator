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
[shell loop](#shell-loop) still wraps both commands when you want
`diff --live` in the same gate.

## Shell loop

[`convctl-fleet.sh`](convctl-fleet.sh) is a copy-pasteable wrapper. It
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

## GitHub Actions matrix

[`convctl-fleet.gha.yml`](convctl-fleet.gha.yml) is a reference workflow,
not a job this repository runs. Copy it into your platform repo and
replace the `context` matrix with your fleet. Each matrix leg is one
cluster; `actions/upload-artifact` collects the JUnit files so a
test-reporter can show a per-cluster breakdown.

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
      - name: Install convctl
        run: |
          go install github.com/terasky-oss/declarative-conversion-operator/cmd/convctl@latest
      - run: |
          convctl compat \
            --base "origin/${{ github.base_ref }}" --head HEAD \
            --config config.yaml --xrd xrd.yaml
```

Exit 1 on a breaking change, 0 once it is acknowledged with `--allow <class>`.
Acknowledging is per class, so allowing a deliberate hub promotion does not
also allow a dropped rule. See [the class table](../cli.md#convctl-compat).
