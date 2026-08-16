# Flux: operator then configs

Two `Kustomization`s. The second `dependsOn` the first and waits, so Flux
does not declare the environment ready until `ConversionWebhookServer/default`
exists and the sample config can go `Applied`.

```text
examples/gitops/flux/operator   HelmRepository + HelmRelease (CRDs + CWS)
examples/gitops/configs         XRD + XRDConversionConfig
```

## Apply

Point a Flux `GitRepository` at this repository (or a fork) and apply
[`clusters/kustomizations.yaml`](clusters/kustomizations.yaml). Adjust
`spec.url` / `spec.path` prefixes if you vendor the tree.

```console
kubectl apply -f examples/gitops/flux/clusters/kustomizations.yaml
kubectl -n flux-system wait --for=condition=Ready --timeout=10m \
  kustomization/conversion-operator
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default
kubectl -n flux-system wait --for=condition=Ready --timeout=5m \
  kustomization/conversion-configs
kubectl get xrdconversionconfig xbuckets-conversion
```

`install.crds: CreateReplace` / `upgrade.crds: CreateReplace` on the
`HelmRelease` is what keeps chart CRDs moving on upgrade. Without that,
Helm's default "CRDs only on first install" leaves GitOps stuck on the
install-time CRDs.

## driftPolicy

The sample config sets `KeepServingStale`. Do not switch it to
`FailClosed` under Flux: a hub flip is two objects, and Flux will
reconcile them in an order you do not control. See the
[parent README](../README.md#driftpolicy-in-continuous-delivery).
