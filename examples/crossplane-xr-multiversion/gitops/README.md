# GitOps: retarget XRs without naming every object

The staged walkthrough in the parent directory patches
`spec.crossplane.compositionRef` on every XR when the hub moves. That is what
Crossplane requires if you manage the pin yourself. This directory is the
GitOps-shaped alternative: **platform git** owns the XRD, Compositions, and
Kyverno policies; **app git** owns XRs that never name a Composition.

```console
# Same lifecycle as the parent demo; retarget via these policies:
../demo.sh --demo-mode gitops

../demo.sh --demo-mode gitops -n    # no pauses
../demo.sh --cleanup

# Live GitOps (GitHub PRs + in-cluster runner + Flux or Argo):
../demo.sh --demo-mode gitops --gitops-engine flux --create-repo
../demo.sh --demo-mode gitops --gitops-engine argo \
  --github-repo "$USER/platform" --git-prefix xwidget-demo
../demo.sh --cleanup --delete-repo   # deletes the GitHub repo only if this run created it
```

`make dev-up` installs Kyverno by default (`DEV_KYVERNO=true`). It does **not**
install Flux, Argo, or the Actions runner — the demo owns those. The parent
[`demo.sh`](../demo.sh) default is still `--demo-mode patches` (kubectl patch
loops). `--demo-mode gitops` retargets with the policies in this directory.
`--gitops-engine` defaults to `simulate` (direct apply, no GitHub).

## Why not `enforcedCompositionRef`

XRD `spec.enforcedCompositionRef` is **immutable** (`self == oldSelf`). You can
set it once; you cannot point it at `xwidgets-v3.example.org` when the hub
flips. Do not use it to chase hub versions.

`defaultCompositionRef` only helps **new** XRs. Existing objects keep the pin
Crossplane wrote at create time. `compositionUpdatePolicy: Automatic` and
`compositionRevisionSelector` only walk **revisions of that pinned
Composition**. They cannot hop to a new Composition (`compositeTypeRef` is
immutable, so the hub flip is a new object).

## What the policies do

`convctl generate kyverno` prints two `policies.kyverno.io/v1` MutatingPolicies.
Checked-in copies live in [`policies/`](policies/) so you can read them without
running the CLI. [`label-compositions-xwidgets.yaml`](policies/label-compositions-xwidgets.yaml)
is the standing labeler alone (apply once). The `from-v*-to-v*.yaml` files are
the full generate output (labeler + migrate) for each hub flip:

```console
convctl generate kyverno --xrd ../03-promote-v2/xrd.yaml --from v1 --to v2
convctl generate kyverno --xrd ../05-promote-v3/xrd.yaml --from v2 --to v3
```

**Composition labeler** (`label-compositions-xwidgets`) is per XRD, not
cluster-wide. Admission writes `xrd-api-version` from
`spec.compositeTypeRef.apiVersion` (`example.org/v2` → `v2`). That label is
**never** in git. Kind + group targeting lives in the mutation CEL (Kyverno
1.18 silently ignores `matchConditions` that read `object.spec`). XRDs that
never evolve their API do not get this policy.

**XR migrate** (`set-composition-version-selector-xwidgets`) is admission +
`mutateExisting`. Kyverno 1.18.1 never runs MutatingPolicy `mutateExisting`
in the background, so admission is what actually strips `compositionRef` /
`compositionRevisionRef` and sets `compositionSelector.matchLabels.xrd-api-version`.
Admin selector keys (`variant: standard`, …) are left alone. `--from` is a
canary (only XRs still on that version, or with no selector). Omit `--from` to
move anyone not already on `--to`.

One MutatingPolicy per XRD. A hub flip updates `--from` / `--to` on that
object (same `metadata.name`); do not create `migrate-*-to-vN` and delete
the previous one. The match CEL uses `oldObject` on UPDATE so a later
`compositionRef` pin write does not rematch once the selector is `--to`.
The `from-v*-to-v*.yaml` files are `convctl generate` snapshots for each
flip; live git writes the migrate document to
`set-composition-version-selector-xwidgets.yaml`.

After the pin is gone, Crossplane re-selects a Composition whose labels match.
`Automatic` writes a new `compositionRevisionRef`. That write also persists the
XR at the new `referenceable` version.

If more than one Composition matches the selector, Crossplane picks at random.
A version-only selector is safe only when there is **one** Composition per hub
version (this example). Keep extra matchLabels if you have variants.

## Repos

| Tree | Who | What changes on hub promote |
|---|---|---|
| [`platform/`](platform/) | Platform | New Composition + XRD/conversion config + updated migrate policy (`--to` the new hub). The labeler is applied once. |
| [`apps/widget.yaml`](apps/widget.yaml) | App team | Nothing. No `compositionRef`. |

Platform snapshots are the parent stage directories (see
[`platform/README.md`](platform/README.md)). Composition names stay versioned
(`xwidgets-v2.example.org`) so `compositeTypeRef` does not require
delete+recreate.

## Apply order on promote

1. Apply the new platform snapshot (XRD, conversion config, new Composition).
2. The per-XRD labeler (already in cluster) sets `xrd-api-version` on the new
   Composition from `compositeTypeRef`.
3. Update the standing migrate policy (`--from` / `--to` the new hub). Same
   `metadata.name`; Kyverno admission retargets live XRs.
4. Crossplane re-selects. Wait `Synced` / `Ready`.

[`policies/kyverno-rbac.yaml`](policies/kyverno-rbac.yaml) aggregates view
onto the reports, background, and admission controllers, and patch/update
onto background + admission. `RBACPermissionsGranted` is a reports check
(`get/list/watch` on every served XR version). Skip the reports label and
the migrate policy stays `Ready=false` even when background can patch.

## GitOps ignore rules

Crossplane writes `compositionRef` and `compositionRevisionRef` onto the live
XR. Treat them like `resourceRefs`:

Argo CD:

```yaml
ignoreDifferences:
  - group: example.org
    kind: XWidget
    jsonPointers:
      - /spec/crossplane/compositionRef
      - /spec/crossplane/compositionRevisionRef
```

Flux SSA keeps those fields under Crossplane’s field manager if they are absent
from git — do not also ignore the selector; that is the desired-state lever
the migrate policy writes.

Do not `ignoreDifferences` the selector if you want a later git change of
`xrd-api-version` to be visible. In this example the selector is written by
Kyverno, not by app git.

## Live engines (`--gitops-engine flux|argo`)

`simulate` is the default: the walkthrough `kubectl apply`s this tree, same as
before. `flux` and `argo` push each stage to a GitHub repo as a PR, wait for
CI, squash-merge, then wait for the sync engine.

```console
../demo.sh --demo-mode gitops --gitops-engine flux --create-repo
../demo.sh --demo-mode gitops --gitops-engine argo --github-repo owner/name --git-prefix xwidget-demo
```

| Flag | Meaning |
|---|---|
| `--gitops-engine simulate\|flux\|argo` | Only with `--demo-mode gitops`. `simulate` = no GitHub. |
| `--github-repo owner/name` | Existing repo (must be able to push and register a runner). |
| `--create-repo [name]` | `gh repo create`. Name defaults to `xwidget-lifecycle-demo`. New repos write at `/`. |
| `--git-prefix PATH` | Subdirectory in an **existing** repo so the demo does not overwrite the root. |
| `--repo-visibility public\|private` | New repos only. Default `private`. |
| `--delete-repo` | With `--cleanup`, `gh repo delete` only if **this run** created the repo. Never deletes a `--github-repo` you passed in. |
| `--from-stage N` | Resume the walkthrough at stage `0`–`6` after a failure. Does not wipe the cluster or git. |

Requirements: `gh` installed and authenticated (`gh auth status`, repo +
workflow scopes), plus `git`, `helm`, `docker`, and `kind`.

### Why not ACT

`nektos/act` runs workflows on the laptop. It is not how GitHub sees PRs and
does not demonstrate `gh pr checks`. GitHub-hosted Actions cannot reach kind.
A runner **pod in kind**, registered with
`gh api repos/.../actions/runners/registration-token` and labeled
`xwidget-demo`, is the runner GitHub dispatches to. The pod uses in-cluster
SA credentials so `convctl test --live` hits the same apiserver.

`demo.sh` builds this checkout's `convctl`, `kind load`s a tiny image, and an
init container copies the binary onto `ghcr.io/actions/actions-runner`. The
workflow in [`workflow/convctl.yaml`](workflow/convctl.yaml) is seeded into
the demo repo.

### What each live stage does

`gitops_ship` (see [`lib.sh`](lib.sh)) copies that stage's XRD / Composition
(and policies on promote) into the worktree `platform/`, and the conversion
config into `conversion/`. One PR contains both so CI can `convctl validate`
a multi-version XRD. Flux applies `platform/` first (`wait: true` on the XRD
and policies only), then `conversion/` (`dependsOn` platform). Admission
validates the config against the *live* XRD, so a single snapshot cannot add
a spoke and the config that names it. App XRs are a third Kustomization
(`apps/`, `wait: false`) — Flux kstatus reports converted XWidgets as
NotFound, which must not block platform Ready.

CI runs `convctl test --samples apps/` (objects of this XRD's kind only) and
`convctl test --live`. Dropping a spoke while an app XR still declares that
`apiVersion` is an ERROR — bump the manifest (stage 6 moves `demo` from v1
to v2) in the same PR that removes the spoke.

One intentional mistake (`mistakes/02-missing-rename.yaml`) is opened as a PR
so you see CI fail and comment the `convctl` output. The walkthrough then
pushes the valid config to the **same** PR and merges when checks go green.
`convctl migrate-storage` in stage 6 stays a **local** command.

### Cleanup

`--cleanup` always removes demo XRs / XRD / policies. If this process
installed Flux, Argo, or the runner, those namespaces go too.
`--delete-repo` only deletes a repo this run created (`CREATED_REPO=1` in
[`.demo-state`](.demo-state), gitignored). It never deletes `--github-repo`.
