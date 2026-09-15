# Deprecation policy

What "beta" promises, what it does not, and how anything user-facing goes away.

## What is stable today

| Surface | Stability | What that means |
|---|---|---|
| CRD API version (`terasky.com/v1alpha1`) | **Frozen at `v1alpha1`** | The group and version are not changing. Fields are added compatibly; an existing field's meaning does not change under you. Promoting to `v1beta1`/`v1` is deliberately out of scope for the current roadmap, because the churn would cost more than the label is worth. |
| Conversion strategy names and their `*Params` | **Stable** | A shipped strategy converts the way it converts. This is the strongest guarantee here, and it has to be: somebody's `XRDConversionConfig` is a description of a migration that already happened. |
| `status` conditions and `status` fields | **Stable** | Conditions are added, not repurposed. Automation keying on `Applied` or `ConversionPropagated` keeps working. |
| Metric names and labels | **Stable, additive** | New metrics and new label *values* appear; an existing metric is not renamed or relabelled without the deprecation path below. |
| Helm values | **Beta** | May change in a minor release, with the deprecation path below. |
| `convctl` flags and output | **Beta** | Same. Human-readable output formats are not an API; `--output json`/`sarif` are treated as one. |
| `manager` / `webhook-server` container flags | **Beta** | These are set by the chart and by `ConversionWebhookServer.spec`, so most users never name one directly. |

Everything in `internal/` is internal. `pkg/engine`, `pkg/xrdadapter`, and
`pkg/crdadapter` are importable and used by `convctl`, but they are not yet
covered by a compatibility promise — if you depend on them, pin a version.

## How something is removed

For anything marked **Beta** above:

1. **Announce.** The release notes for the release that deprecates it say what
   is going away, what replaces it, and when it stops working. The deprecated
   thing keeps working exactly as before.
2. **Warn where it is used.** A deprecated Helm value renders a warning in
   `NOTES.txt`; a deprecated CLI flag prints to stderr and keeps working; a
   deprecated field on one of this operator's CRDs produces an admission
   warning (not a rejection).
3. **Remove, no sooner than two minor releases later.** A thing deprecated in
   `0.n` is removed no earlier than `0.n+2`. If a removal would silently change
   behaviour rather than fail loudly, it waits longer or does not happen.

A removal that would fail *silently* — a values key that stops being read, a
flag that stops being honoured — is the case this policy mostly exists for.
Since the chart ships a `values.schema.json` with `additionalProperties: false`,
a removed values key is rejected at `helm install` rather than ignored, which
turns the worst case into a loud one.

## What is not covered

- **Security fixes.** A change required to fix a vulnerability ships as soon as
  it is ready, and the release notes say so. The steps above are a courtesy to
  working configurations, not a reason to leave one exploitable.
- **Behaviour that was a bug.** If conversion produced the wrong object, fixing
  it changes behaviour on purpose. Release notes call it out under its own
  heading.
- **Anything in `docs/limitations.md`.** Those are stated non-guarantees.

## Related

- [Limitations](limitations.md) — what the project deliberately does not do.
- [Upgrade runbook](operations/upgrade-runbook.md) — the mechanics of moving between releases.
- [Roadmap](roadmap.md) — where the surfaces above are heading.
