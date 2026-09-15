# `convctl-test`

Runs `convctl test` and puts the result where a reviewer will see it.

```yaml
# convctl comes from setup-convctl, once per job.
- uses: terasky-oss/declarative-conversion-operator/.github/actions/setup-convctl@v1
- uses: terasky-oss/declarative-conversion-operator/.github/actions/convctl-test@v1
  with:
    config: apis/widgets/conversion.yaml
    xrd: apis/widgets/xrd.yaml
    samples: apis/widgets/samples/
    validate-output: "true"
```

Three outputs beyond the exit code:

- a **JUnit artifact**, uploaded with `always()` — the report is most worth
  having when the step failed, which is exactly when a naive step after a
  failing one would not run;
- a **job summary** table of passed / unacknowledged-loss / error counts,
  which also says so when a `--live` run was sampled;
- **annotations** on the pull-request diff, pointing at the line of the config
  that produced each finding.

The annotations come from `convctl --output github`, which emits the workflow
commands itself. This Action relays them; it does not parse YAML to re-derive
a location the tool already knows.

## Inputs

Schema source: `xrd`, `crd`, or `package` (+ `target`).
Samples: `samples`, or `live` (+ `kubeconfig`, `context`, `max-samples`,
`sample-strategy`).
Behaviour: `fail-on`, `strict`, `validate-output`, `concurrency`, `version`,
`upload-artifact`, `artifact-name`, `annotate`.

## Outputs

`exit-code` (convctl's own, preserved), `report-path`, `pass`,
`unacknowledged-loss`, `errors`.

## Secrets

`kubeconfig` is written with `umask 077` **before** the file is created rather
than `chmod`-ed afterwards — between creation and chmod the file is briefly
world-readable — and is never echoed.

## Requires `setup-convctl`

This Action consumes `convctl` from `PATH` and does not install it. Run
[`setup-convctl`](../setup-convctl) first — once per job, however many of
these Actions follow.

That is not an ergonomic preference. A composite action cannot reference a
local action by path once published: `./…` resolves against the **consumer's**
workspace, so a nested setup step would work in this repository's own tests
and fail for everyone else.
