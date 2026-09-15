# `convctl-fleet`

Runs `convctl test --live` against every cluster in a fleet and aggregates
the result into one JUnit report, with one `<testsuite>` per cluster.

```yaml
- uses: terasky-oss/declarative-conversion-operator/.github/actions/convctl-fleet@v1
  with:
    config: apis/widgets/conversion.yaml
    xrd: apis/widgets/xrd.yaml
    contexts: prod-us,prod-eu
    kubeconfig: ${{ secrets.FLEET_KUBECONFIG }}
```

Either `contexts` (comma-separated, from one kubeconfig) or `kubeconfig-dir`
(one file per cluster).

## An unreachable cluster is a failed suite

Not a skip. `convctl` already behaves this way; the Action surfaces it, in
the report and in the `failed-clusters` output. A fleet check that quietly
covered four of five clusters and reported green is worse than one that did
not run at all.

## Alternative: a matrix

`convctl-fleet` runs the clusters in one job. A per-cluster matrix using
[`convctl-test`](../convctl-test) gives you one job per cluster — slower to
set up, but a clearer failure surface and parallel execution. Use
`fail-fast: false` either way. Both shapes are in
[`convctl-fleet.gha.yml`](../../../docs/gitops/convctl-fleet.gha.yml).

## Outputs

`exit-code`, `report-path`, `clusters`, `failed-clusters`.
