# Branch map (union-typed fields)

`xbackups.example.org` stores a backup destination as a **union**: exactly one
of two object-typed branches, with a `provider` discriminator naming which. The
two versions disagree about every part of it — the branch names (`s3`/`gcs` vs
`awsBucket`/`googleBucket`), the discriminator values (`s3`/`gcs` vs
`aws`/`google`), and the field inside each branch (`bucket` vs `name`).

| File | What it is |
|---|---|
| `xrd.yaml` | The XRD. `spec.destination` is a `oneOf` union on both versions. |
| `xrdconversionconfig.yaml` | One `BranchMap` rule with nested per-branch rules, plus one ordinary `FieldRename` next to it. |
| `samples/` | One object at each served version, each with a different branch set. |

## The interesting part

**A union in a CRD is not a hidden shape.** The apiserver requires every
property named inside a `oneOf` to also be declared in the parent's own
`properties`, so a branch is an ordinary declared, optional field with a path.
That is why `branchMap` identifies the active branch by *which branch property
is present*, rather than by validating the object against each branch schema —
and why the engine can see, and insist on covering, every leaf inside a branch.

Three things follow from that, all visible in this example:

- **`hubPath`/`spokePath` name the union object, not a branch.** The rule needs
  to see all the branches at once to know which one is set.
- **Nested rules are scoped to the branch.** `hubPath: bucket`, not
  `hubPath: spec.destination.s3.bucket` — the same scoping `forEach` gives an
  array element. `region` and `location` keep their shape on both sides, so
  they need no rule inside their branches.
- **`branchMap` writes each branch at its own path, not the union object
  wholesale.** `spec.destination.retentionDays` → `spec.destination.retention`
  is a plain `FieldRename` sitting alongside, because the rule claimed only the
  branches and the discriminator. Setting the whole object would have made the
  result depend on which of the two rules ran first.

**Converting a union with no branch set, or with two, is a hard error** — in
both directions:

```text
branchMap: branches [gcs s3] are all set at "spec.destination"; exactly one must be
```

The alternative is worse than it sounds. An object with no branch converts to
an object with no branch, and the *destination's* `oneOf` then rejects it at
admission — with a message about the schema, arriving after the conversion that
caused it, pointing at the wrong thing.

## Run it

```console
convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl test     --config xrdconversionconfig.yaml --xrd xrd.yaml --samples ./samples/
```

Both conversions are lossless, so the report is four `PASS` results and no
acknowledged loss. Two edits worth trying:

- **Drop one branch mapping.** `validate` reports the fields inside the
  unmapped branch as uncovered, on both sides — an unmapped branch is an
  ordinary coverage error, not a lossiness question.
- **Point both hub branches at `awsBucket`.** Collapsing branches is
  expressible, but it cannot be undone: coming back, the engine cannot tell
  which branch it started from, so `validate` demands `acknowledgeLossy: true`
  — the same verdict `enumRemap` gives a non-injective mapping. (In *this*
  schema you would also have to say what becomes of `gcs.location` and of the
  now-unmapped `googleBucket`, because a collapse only makes sense when the
  branches it merges have a shape the survivor can hold.)

## Reference

- [Branch Map strategy](https://terasky-oss.github.io/declarative-conversion-operator/strategies/branch-map/)
