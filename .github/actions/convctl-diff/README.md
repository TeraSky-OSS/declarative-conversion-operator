# `convctl-diff`

Runs `convctl diff` and upserts the delta as a **sticky** pull-request
comment — updated in place as the branch changes, rather than appended to on
every push.

```yaml
permissions:
  contents: read
  pull-requests: write

steps:
  - uses: terasky-oss/declarative-conversion-operator/.github/actions/convctl-diff@v1
    with:
      config: apis/widgets/conversion.yaml
      xrd: apis/widgets/xrd.yaml
      live: "true"
      kubeconfig: ${{ secrets.KUBECONFIG_PROD }}
```

## Exit codes are not all failures

`convctl diff` exits 1 when it finds a delta. That is the thing being
reported, not a failure, so **the job stays green by default**. Exit 2 — a
usage error, or a cluster it could not reach — always fails, because a gate
that reported "no deltas" when it could not run would be silently useless.

Set `fail-on-delta: true` to make any delta fail the job.

## A comment that says "no deltas" rather than vanishing

When there is nothing to report, the comment is edited to say so. A comment
that disappears reads as *"the check stopped running"*, which is the wrong
message to send about a check that ran and passed.

## Multiple configs in one pull request

`comment-tag` identifies the comment to update, and defaults to the config
path — so two configs in one pull request get one comment each with no
configuration. Set it explicitly if you want something else.

## Forks

A `pull_request` event from a fork has a read-only token. The Action emits a
**notice** and leaves the delta in the job summary, rather than failing: a red
check a contributor cannot fix teaches them to ignore red checks.

Needs `pull-requests: write` to comment.
