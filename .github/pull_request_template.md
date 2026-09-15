<!--
Thanks for contributing. The checklist below is the same one a reviewer
will work through, so filling it in usually makes the review shorter.
-->

## What this changes

<!-- One or two sentences. What behaviour is different after this merges? -->

## Why

<!--
The problem, not the patch. If there is an issue, link it and say
"Closes #N" so it closes on merge.
-->

## How it was verified

<!--
Which of the checks below actually ran, and what they showed. "make test"
alone is rarely enough for anything touching the conversion path.
-->

## Checklist

- [ ] `make test` passes (`generate` + `manifests` + `fmt` + `vet` + `go test -race`)
- [ ] `make lint` is clean, and any `//nolint` added carries a comment saying why
- [ ] Generated CRDs are in sync (`make helm-sync`) if `api/` changed
- [ ] Docs updated if user-facing behaviour changed — CLI flags, chart values,
      status semantics, strategies, or limitations
- [ ] New or changed behaviour has a test that fails without the change
- [ ] If this touches the conversion hot path, admission, or the webhook
      server's rollout: the relevant e2e (`make test-e2e*`) or
      `make test-e2e-soak` was run, and the result is stated above
- [ ] If this adds or changes a metric or alert: the metric catalogue in
      `docs/observability.md` and `make test-prometheus` were updated

## Anything a reviewer should look at first

<!--
Optional. The part you are least sure about, a deliberate trade-off, or a
deviation from what an issue asked for and why.
-->
