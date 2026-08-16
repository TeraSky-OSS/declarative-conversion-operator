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

A fleet gate should fail the PR if **any** cluster returns 1 or 2. A
delta on `diff` is not automatically a bug — it is the change you are
about to roll out. Typical pattern: require `test --live` green on every
cluster, and treat `diff --live` as a required review artifact (upload
the JSON) unless you are enforcing "no accidental coverage change."

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
