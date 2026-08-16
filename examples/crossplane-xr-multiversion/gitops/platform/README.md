# Platform snapshots

These are the parent-directory stage trees. They are not copied here so the
XRD, conversion config, and Composition stay a single source of truth.

| GitOps revision | Apply |
|---|---|
| v1 only | [`../../01-v1-only/`](../../01-v1-only/) (`xrd.yaml`, `composition.yaml`) |
| Add v2 spoke | [`../../02-add-v2/`](../../02-add-v2/) |
| Promote v2 | [`../../03-promote-v2/`](../../03-promote-v2/) |
| Add v3 spoke | [`../../04-add-v3/`](../../04-add-v3/) |
| Promote v3 | [`../../05-promote-v3/`](../../05-promote-v3/) |

In a real platform repo you would copy those files (or vendor this example)
next to [`../policies/`](../policies/). Composition names stay versioned
(`xwidgets-v2.example.org`) because `compositeTypeRef` is immutable.

`--gitops-engine flux|argo` does that copy into the demo GitHub repo
(`platform/` + `apps/`) via [`../lib.sh`](../lib.sh). Simulate mode keeps
applying the parent stage directories directly.
