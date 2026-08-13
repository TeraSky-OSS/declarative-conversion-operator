# Upgrade runbook

Three things get upgraded independently, and only one of them is risky:

| What | Risk | Why |
|---|---|---|
| A conversion config (`XRDConversionConfig`/`CRDConversionConfig`) | **Highest** — it changes how live objects are converted. | Validated before anything is patched, but a valid config can still be the *wrong* mapping. |
| The Helm chart / images | Moderate — the conversion data path restarts. | Conversions are served by webhook-server pods; a rollout briefly cycles them. |
| The CRDs in the chart | Moderate, and manual. | Helm applies `crds/` once at install and never touches it again. |

## Upgrading a conversion config

The config is the only part of this system that can silently start converting
data differently. Both checks below run entirely offline against the live
cluster's schema, need no write access, and exit non-zero on a problem, so they
drop straight into CI.

```console
# What does this edit change, in coverage/lossiness terms rather than YAML lines?
convctl diff --config new-config.yaml --live

# Does it still hold up against every object that already exists?
convctl test --xrd xrd.yaml --config new-config.yaml --live
```

`convctl diff --live` treats the cluster as the *from* side and your file as the
*to* side, so it reads as "what applying this would change": fields that become
uncovered, rule claims added or removed, lossless flags that flipped. If no
config of that name exists in the cluster yet, every rule shows up as an
addition.

`convctl test --live` is the one that matters most before a change lands: your
fixtures are what you thought of, and the cluster is what people actually
created.

Then apply and watch it through:

```console
kubectl apply -f new-config.yaml
kubectl get xrdconversionconfig <name> -w    # PHASE should settle on Applied
```

If validation fails, the phase goes `Invalid` and **the target is never
patched** — the previously applied plan keeps serving. That makes a bad config
edit a recoverable non-event rather than an outage.

## Upgrading the chart

### 1. Record what you're running

```console
helm list -n declarative-conversion-system
helm get values declarative-conversion-operator -n declarative-conversion-system
```

### 2. Check whether the CRDs changed

CRDs live in the chart's `crds/` directory and follow [Helm's
convention](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/):
applied once at install, **never** touched by `helm upgrade` or
`helm uninstall`. That's the safest default against schema-change data loss, and
it means a CRD schema change is a step you take yourself, *before* the upgrade —
otherwise the new manager runs against the old schema and any new field is
silently dropped by the apiserver.

```console
# Fetch once; fail here if Helm cannot retrieve the chart version.
# From a checkout of the version you're moving to:
make helm-upgrade-crds          # kubectl diff; exits 1 if they differ
make helm-upgrade-crds APPLY=1  # apply only after you've inspected the diff

# Equivalent against a published chart (what the Make target wraps):
./hack/upgrade-crds.sh \
  --chart oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator \
  --version <new-version>
./hack/upgrade-crds.sh \
  --chart oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator \
  --version <new-version> --apply
```

`hack/upgrade-crds.sh` is the same sequence as below (`helm show crds`, then
`kubectl diff`, then optional `kubectl apply`). Do not pipe `helm show crds`
straight into `kubectl apply` without checking retrieval succeeded — a Helm
pull failure would otherwise look like an empty apply.

```console
# Fetch once; fail here if Helm cannot retrieve the chart version.
if ! helm show crds oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator \
  --version <new-version> > /tmp/dco-crds.yaml; then
  echo "helm show crds failed; not applying CRDs" >&2
  exit 1
fi
if [ ! -s /tmp/dco-crds.yaml ]; then
  echo "helm show crds returned empty output; not applying CRDs" >&2
  exit 1
fi

# kubectl diff exits 0 (identical), 1 (diff), or >1 (tooling error).
set +e
kubectl diff -f /tmp/dco-crds.yaml
diff_rc=$?
set -e
if [ "$diff_rc" -gt 1 ]; then
  echo "kubectl diff failed (exit $diff_rc); not applying CRDs" >&2
  exit "$diff_rc"
fi
if [ "$diff_rc" -eq 1 ]; then
  kubectl apply -f /tmp/dco-crds.yaml
fi
```

CRD updates are additive in practice (new optional fields, widened enums), but
`kubectl diff` is what tells you that rather than an assumption. Do not pipe
`helm show crds` straight into `kubectl apply` without checking retrieval
succeeded — a Helm pull failure would otherwise look like an empty apply.

### 3. Upgrade the release

```console
helm upgrade declarative-conversion-operator \
  oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator --version <new-version> \
  --namespace declarative-conversion-system \
  --reuse-values
```

Rendering it first (`helm template ... | kubectl diff -f -`) is worth the extra
minute on a cluster you care about, and needs no cluster write access.

For reproducibility, pin images by digest rather than tag — `image.manager.digest`
and `image.webhookServer.digest` take precedence over `tag` when set. Release
images are cosign-signed with SBOMs.

### 4. Verify

```console
kubectl -n declarative-conversion-system rollout status deploy/declarative-conversion-operator-manager
kubectl wait --for=condition=Available conversionwebhookserver/default --timeout=180s

# Every config should be back at Applied, none Stale or Invalid
kubectl get xrdconversionconfig,crdconversionconfig
```

Then confirm the data path, not just the control plane — a webhook-server
rollout means every replica rebuilt its in-memory registry from scratch:

```promql
# Ready replicas that have NOT loaded a plan for this target (empty = healthy)
(xrdconv_webhook_ready == 1)
  unless on (pod)
(xrdconv_webhook_registry_entry_loaded{target="xwidgets.example.org"} == 1)

# Conversion errors since the rollout
sum by (target, result) (rate(xrdconv_webhook_conversion_review_requests_total{result!="success"}[5m]))
```

A real read through the apiserver is the final proof, since it exercises the
whole path including TLS:

```console
kubectl get <resource>.<old-version>.<group> <name> -o yaml
```

### 5. If it goes wrong

```console
helm rollback declarative-conversion-operator -n declarative-conversion-system
```

Two caveats. `helm rollback` does **not** revert CRDs — if step 2 applied a new
schema, it stays; that's normally harmless because CRD changes are additive.
And rolling back does not un-patch `spec.conversion` on your XRDs/CRDs: they
keep pointing at the same webhook Service, which the rolled-back release still
provides.

Conversions are served by the webhook-server pods, so a manager problem is not a
data-path outage. What a manager outage *does* block is applying new
`XRDConversionConfig`/`ConversionWebhookServer` objects, because its admission
webhook has `failurePolicy: Fail`.

## Upgrading Kubernetes

The apiserver calls the conversion webhook on reads and writes of any object at
a non-storage version, so keep it available across a control-plane or node
upgrade:

- Run the default `ConversionWebhookServer` at **2+ replicas** with a
  `PodDisruptionBudget` (both are chart defaults) so a node drain can't take the
  last replica down. See the [HA checklist](ha-checklist.md).
- After the upgrade, re-run the registry-readiness query above: every replica
  that came back on a new node recompiled its registry from scratch.

## Post-upgrade checklist

- [ ] Every config is `Applied` — none `Invalid`, `Stale`, or `Failed`.
- [ ] `ConversionWebhookServer` is `Available` with the expected `readyReplicas`.
- [ ] Every ready replica has a loaded plan for every target.
- [ ] No new `xrdconv_webhook_registry_compile_errors_total` since the rollout.
- [ ] Conversion error ratio and p99 latency match pre-upgrade levels.
- [ ] A real `kubectl get` at a non-storage version returns a correctly
      converted object.

## Related

- [Installation](../installation.md) — install-time values and the CRD convention.
- [Troubleshooting](troubleshooting.md) — for anything on that checklist that
  didn't pass.
- [CLI Reference](../cli.md) — `convctl diff` / `test --live` flags and exit codes.
