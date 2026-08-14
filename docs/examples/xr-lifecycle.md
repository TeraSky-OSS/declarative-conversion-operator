# Crossplane XR version lifecycle

The staged example under
[`examples/crossplane-xr-multiversion/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion)
walks an XRD from a single version through a conversion webhook, two hub
promotions (`v2` then `v3` as the standard), deprecating `v1`, rewriting etcd
at the new storage version, and dropping the `v1` block. Each directory is a
complete snapshot.

The Composition emits a native `ConfigMap` via `function-go-templating` and
marks the XR ready with `function-auto-ready`. There is no cloud provider and
no `provider-kubernetes`.

On a cluster with Crossplane and this operator already installed (`make
dev-up`), [`demo.sh`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/examples/crossplane-xr-multiversion/demo.sh)
is a demo-magic walkthrough: each command is typed out, and `convctl` is run
against intentional mistakes before the good config is applied.

```console
./examples/crossplane-xr-multiversion/demo.sh          # Enter to type, Enter to run
./examples/crossplane-xr-multiversion/demo.sh -n       # no pauses
./examples/crossplane-xr-multiversion/demo.sh --cleanup
```

| Stage | Snapshot | What to notice |
|---|---|---|
| 1. One version | [`01-v1-only/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/01-v1-only) | XRD + Composition only. No conversion config. |
| 2. Add `v2` as a spoke | [`02-add-v2/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/02-add-v2) | `referenceable` stays on `v1`. `FieldRename` `spec.size` ↔ `spec.capacity`. |
| 3. Promote `v2` | [`03-promote-v2/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/03-promote-v2) | Flip `referenceable`, rewrite rules, new Composition, retarget every XR `compositionRef`. |
| 4. Add `v3` as a spoke | [`04-add-v3/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/04-add-v3) | Same pattern as stage 2, now with hub `v2`. |
| 5. Promote `v3` | [`05-promote-v3/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/05-promote-v3) | `v3` becomes the hub (the new standard). New Composition + retarget `compositionRef`. |
| 6. Deprecate `v1` | [`06-deprecate-v1/`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/examples/crossplane-xr-multiversion/06-deprecate-v1) | Drop `v1` from the conversion config, `served: false`, prune `storedVersions` (`compositionRef` retarget already rewrote etcd — unlike a native CRD), then [`xrd-drop-v1.yaml`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/examples/crossplane-xr-multiversion/06-deprecate-v1/xrd-drop-v1.yaml) to remove the version block. `v2` stays a spoke. |

Offline checks (from a repo checkout):

```console
convctl test --config examples/crossplane-xr-multiversion/04-add-v3/xrdconversionconfig.yaml \
  --xrd examples/crossplane-xr-multiversion/04-add-v3/xrd.yaml \
  --samples examples/crossplane-xr-multiversion/04-add-v3/samples/

# Draft the stage-5 config from stage 4 (review before apply):
convctl rehub --config examples/crossplane-xr-multiversion/04-add-v3/xrdconversionconfig.yaml \
  --xrd examples/crossplane-xr-multiversion/04-add-v3/xrd.yaml --to v3
```

The full apply walkthrough, including Composition pipeline YAML and hub-promotion
ordering, lives in the [example README](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/examples/crossplane-xr-multiversion/README.md).
Hub-promotion safety (`KeepServingStale`) is documented in
[XRDConversionConfig: Changing the hub version](../configuration/xrdconversionconfig.md#changing-the-hub-version).
Use [`convctl rehub`](../cli.md#convctl-rehub) as the draft step when rewriting rules for a new hub.
