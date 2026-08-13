# Multi-version Crossplane XR lifecycle

This is a **staged walkthrough**, not a dump of the end state. Each directory
is a complete snapshot you can apply (or `convctl`-test) on its own. The
composed resource is a native `ConfigMap` — no cloud provider, no
`provider-kubernetes`.

The API starts as `XWidget` `v1` (`spec.widgetName`, `spec.size`). `v2` renames
`size` → `capacity`. `v3` renames `widgetName` → `name`. The composition always
reads the **hub** (referenceable) version and writes those fields onto a
ConfigMap.

| Stage | Directory | What changes |
|---|---|---|
| 1 | [`01-v1-only/`](01-v1-only/) | One-version XRD + Composition. No conversion webhook. |
| 2 | [`02-add-v2/`](02-add-v2/) | Serve `v2` without making it the hub. Add `XRDConversionConfig`. |
| 3 | [`03-promote-v2/`](03-promote-v2/) | Flip `referenceable` to `v2`, rewrite conversion rules, create a new Composition, retarget every XR's `compositionRef`. |
| 4 | [`04-add-v3/`](04-add-v3/) | Serve `v3` the same way `v2` was added in stage 2. |
| 5 | [`05-promote-v3/`](05-promote-v3/) | Promote `v3` to the hub (the new standard). New Composition + retarget `compositionRef`. |
| 6 | [`06-deprecate-v1/`](06-deprecate-v1/) | Stop serving `v1` and drop it from the conversion config. `v2` remains a spoke. |

## Prerequisites (cluster)

```console
# Crossplane v2, this operator, and two composition functions:
kubectl apply -f functions.yaml
kubectl wait --for=condition=Healthy function/function-go-templating --timeout=120s
kubectl wait --for=condition=Healthy function/function-auto-ready --timeout=120s
```

The pipeline is `function-go-templating` (emits the ConfigMap) then
`function-auto-ready` (ConfigMaps are ready as soon as they exist). Offline
`convctl` checks do not need a cluster or those functions.

## Live demo

[`demo.sh`](demo.sh) is a [demo-magic](https://github.com/paxtonhare/demo-magic)
walkthrough ([`demo-magic.sh`](demo-magic.sh) vendored upstream, unmodified).
Every `kubectl` / `convctl` command is typed out; press Enter to reveal it,
then Enter again to run it. `-n`, `-d`, and `-w` are demo-magic's own flags.
Before each apply it runs `convctl` against an **intentional mistake** so you
see the CLI reject the config before it can hit the cluster. Simulated typing
needs [`pv`](https://www.ivarch.com/programs/pv.shtml); without it the script
passes `-d` for you.

```console
# Interactive (Enter to type, Enter to run)
./examples/crossplane-xr-multiversion/demo.sh

# No pauses — good for recording
./examples/crossplane-xr-multiversion/demo.sh -n

# Print commands instantly (no typing)
./examples/crossplane-xr-multiversion/demo.sh -d

# Auto-advance after 3s
./examples/crossplane-xr-multiversion/demo.sh -w 3

# Wipe leftover XRs / XRD / conversion config
./examples/crossplane-xr-multiversion/demo.sh --cleanup
```

The broken configs live in [`mistakes/`](mistakes/). They are never applied.

The `kubectl` / `convctl` commands from here on are relative to this directory:

```console
cd examples/crossplane-xr-multiversion
```

---

## Stage 1 — one version, no conversion

[`01-v1-only/`](01-v1-only/) is a normal Crossplane XRD: a single served,
referenceable `v1`. The Composition's `compositeTypeRef` is `example.org/v1`
and the template reads `spec.widgetName` / `spec.size`. The XRD sets
`spec.defaultCompositionRef` so the XR does not need its own
`spec.crossplane.compositionRef`.

```console
kubectl apply -f 01-v1-only/xrd.yaml
kubectl wait --for=condition=Established crd/xwidgets.example.org --timeout=60s
kubectl apply -f 01-v1-only/composition.yaml
kubectl apply -f 01-v1-only/xr.yaml

kubectl get configmap demo-settings -n default -o yaml
# data.widgetName: demo-widget
# data.size: Large
```

There is nothing for this operator to do yet. A one-version XRD does not need a
conversion webhook.

---

## Stage 2 — add `v2` as a spoke (not the hub)

[`02-add-v2/`](02-add-v2/) adds `v2` with `served: true` and
`referenceable: false`. Clients can create `example.org/v2` objects immediately;
storage and the Composition stay on `v1`.

The conversion config is expressed **from the current hub**:

```yaml
hubVersion: v1
spokes:
  - version: v2
    rules:
      - strategy: FieldRename
        fieldRename:
          hubPath: spec.size
          spokePath: spec.capacity
```

`widgetName` is identical on both sides, so it needs no rule. The Composition
is unchanged — it still targets `example.org/v1`.

```console
# Offline, before applying:
convctl validate --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml
convctl test     --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml \
  --samples 02-add-v2/samples/

kubectl apply -f 02-add-v2/xrd.yaml
kubectl apply -f 02-add-v2/xrdconversionconfig.yaml
kubectl get xrdconversionconfig xwidgets-conversion -w   # wait for Applied

# Create at v2; read back at the hub:
kubectl apply -f - <<'EOF'
apiVersion: example.org/v2
kind: XWidget
metadata: {name: from-v2, namespace: default}
spec: {widgetName: from-v2, capacity: Large}
EOF
kubectl get xwidgets.v1.example.org from-v2 -n default -o jsonpath='{.spec.size}'
# Large
```

---

## Stage 3 — promote `v2` to the hub

Two things must move together: the XRD's `referenceable` flag, and
`spec.hubVersion` on the conversion config. The Composition must move too —
`compositeTypeRef` has to name the referenceable version, and the template has
to read the new field names.

**`spec.compositeTypeRef` is immutable.** You cannot patch the v1 Composition
to target `v2`. Create a new Composition (`xwidgets-v2.example.org`) and set
`spec.defaultCompositionRef` on the XRD.

**Retarget every existing XR.** Crossplane writes
`spec.crossplane.compositionRef` when the XR is created and does not move it
when you change `defaultCompositionRef`. After the hub flip, a Composition
whose `compositeTypeRef` is the old version is rejected
(`referenced composition is not compatible` → `Synced=False`). Patch
`compositionRef` (and drop the pinned `compositionRevisionRef`) onto the new
Composition.

[`03-promote-v2/`](03-promote-v2/) is the snapshot after those edits have landed.

**Conversion rules are not a mechanical swap.** They are always hub-relative, so
the old spoke mapping is rewritten from `v2`'s point of view:

| Stage 2 (hub `v1`) | Stage 3 (hub `v2`) |
|---|---|
| `hubPath: spec.size` → `spokePath: spec.capacity` | `hubPath: spec.capacity` → `spokePath: spec.size` |

**Composition changes** in this stage:

| | Before (v1 hub) | After (v2 hub) |
|---|---|---|
| Composition name | `xwidgets.example.org` | `xwidgets-v2.example.org` (new object) |
| `compositeTypeRef` | `example.org/v1` | `example.org/v2` |
| ConfigMap data | `size: {{ .spec.size }}` | `capacity: {{ .spec.capacity }}` |

Existing XRs must have `spec.crossplane.compositionRef` patched to
`xwidgets-v2.example.org`. New XRs pick it up via `defaultCompositionRef`.
Composed ConfigMaps then show `data.capacity`.

The operator's default `driftPolicy: KeepServingStale` means the two XRD/config
updates can land in either order — conversions keep serving the last good plan
until both agree. See [Changing the hub version](https://terasky-oss.github.io/declarative-conversion-operator/configuration/xrdconversionconfig/#changing-the-hub-version).

```console
convctl validate --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml
convctl test     --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml \
  --samples 03-promote-v2/samples/

kubectl apply -f 03-promote-v2/xrd.yaml
kubectl apply -f 03-promote-v2/xrdconversionconfig.yaml
kubectl apply -f 03-promote-v2/composition.yaml
# Patch every XR: spec.crossplane.compositionRef.name = xwidgets-v2.example.org
# and remove spec.crossplane.compositionRevisionRef so Automatic policy selects
# a revision of the new Composition.
```

Objects already stored at `v1` keep working: the apiserver converts them
through the webhook. Rewriting etcd bytes to the new storage version is
optional Kubernetes housekeeping (`kubectl get … -o json | kubectl replace -f -`),
not something this operator does.

---

## Stage 4 — add `v3` the same way as `v2`

[`04-add-v3/`](04-add-v3/) repeats stage 2 against the new hub: `v3` is served
and **not** referenceable. The Composition stays on `v2`. A second spoke is
appended to the conversion config:

```yaml
hubVersion: v2
spokes:
  - version: v1
    rules: [{strategy: FieldRename, fieldRename: {hubPath: spec.capacity, spokePath: spec.size}}]
  - version: v3
    rules: [{strategy: FieldRename, fieldRename: {hubPath: spec.widgetName, spokePath: spec.name}}]
```

`spec.capacity` is identical on `v2` and `v3`, so the `v3` spoke does not
mention it. Spoke-to-spoke (`v1` → `v3`) always hops through the hub.

```console
convctl validate --config 04-add-v3/xrdconversionconfig.yaml --xrd 04-add-v3/xrd.yaml
convctl test     --config 04-add-v3/xrdconversionconfig.yaml --xrd 04-add-v3/xrd.yaml \
  --samples 04-add-v3/samples/

kubectl apply -f 04-add-v3/xrd.yaml
kubectl apply -f 04-add-v3/xrdconversionconfig.yaml
```

Promoting `v3` is the same four edits as stage 3 — that is the next snapshot.

---

## Stage 5 — promote `v3` to the hub

[`05-promote-v3/`](05-promote-v3/) makes `v3` the standard: `referenceable`,
`hubVersion: v3`, Composition `xwidgets-v3.example.org`, and every XR's
`compositionRef` moved onto it.

Rules are rewritten from `v3`'s point of view (`spec.name`, `spec.capacity`):

```yaml
hubVersion: v3
spokes:
  - version: v2
    rules: [{strategy: FieldRename, fieldRename: {hubPath: spec.name, spokePath: spec.widgetName}}]
  - version: v1
    rules:
      - {strategy: FieldRename, fieldRename: {hubPath: spec.name, spokePath: spec.widgetName}}
      - {strategy: FieldRename, fieldRename: {hubPath: spec.capacity, spokePath: spec.size}}
```

The v3 Composition template writes `data.name` / `data.capacity` onto the
ConfigMap.

```console
convctl validate --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml
convctl test     --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml \
  --samples 05-promote-v3/samples/

kubectl apply -f 05-promote-v3/xrd.yaml
kubectl apply -f 05-promote-v3/xrdconversionconfig.yaml
kubectl apply -f 05-promote-v3/composition.yaml
# Patch compositionRef on every XR to xwidgets-v3.example.org
```

---

## Stage 6 — deprecate `v1`

[`06-deprecate-v1/`](06-deprecate-v1/) is after `v1` is no longer part of the
live API. Hub stays `v3`; `v2` remains a served spoke. Do this in order:

1. **Stop creating `v1` objects.** Move clients to `v2` or `v3`.
2. **Drop `v1` from `XRDConversionConfig`** before (or in the same apply as)
   un-serving it. A spoke whose version is `served: false` fails validation
   (`spoke version "v1" is not served`).
3. **Set `served: false` on `v1`** in the XRD. The version block can stay so
   stored objects are still understood; the apiserver stops serving the
   `example.org/v1` endpoint.
4. **Remove the `v1` version block** from the XRD only after stored objects
   have been rewritten at the current storage version (Crossplane's
   `referenceable` version — `v3` here) **and** `v1` is gone from the
   generated CRD's `status.storedVersions`. Kubernetes rejects dropping a
   version that is still listed there; “no live `v1` objects” is not enough
   because `storedVersions` keeps historical versions until you prune it.

   ```console
   kubectl get crd xwidgets.example.org -o jsonpath='{.status.storedVersions}{"\n"}'
   # rewrite remaining objects at the storage version, then prune storedVersions
   kubectl get xwidgets.example.org -A -o json | kubectl replace -f -
   ```

```console
convctl validate --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml
convctl test     --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml \
  --samples 06-deprecate-v1/samples/

kubectl apply -f 06-deprecate-v1/xrdconversionconfig.yaml
kubectl apply -f 06-deprecate-v1/xrd.yaml
```

What remains: hub `v3`, spoke `v2`, Composition `xwidgets-v3.example.org`,
native ConfigMap. Deprecating `v2` later is the same drop-from-config then
`served: false` sequence.

---

## Reference

- [Field Rename](https://terasky-oss.github.io/declarative-conversion-operator/strategies/field-rename/)
- [Changing the hub version](https://terasky-oss.github.io/declarative-conversion-operator/configuration/xrdconversionconfig/#changing-the-hub-version)
- [CLI Reference](https://terasky-oss.github.io/declarative-conversion-operator/cli/) — `convctl test` exit codes and `--live`
- [function-go-templating](https://github.com/crossplane-contrib/function-go-templating) ·
  [function-auto-ready](https://github.com/crossplane-contrib/function-auto-ready)
